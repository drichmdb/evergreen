package units

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/cloud"
	"github.com/evergreen-ci/evergreen/model/distro"
	"github.com/evergreen-ci/evergreen/model/host"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
)

const (
	idleHostJobName           = "idle-host-termination"
	idleWaitingForAgentCutoff = 10 * time.Minute

	// MaxTimeNextPayment is the amount of time we wait to have left before marking a host as idle
	maxTimeTilNextPayment = 5 * time.Minute
)

func init() {
	registry.AddJobType(idleHostJobName, func() amboy.Job {
		return makeIdleHostJob()
	})
}

type idleHostJob struct {
	job.Base        `bson:"metadata" json:"metadata" yaml:"metadata"`
	Terminated      int      `bson:"terminated" json:"terminated" yaml:"terminated"`
	TerminatedHosts []string `bson:"terminated_hosts" json:"terminated_hosts" yaml:"terminated_hosts"`

	env evergreen.Environment
}

func makeIdleHostJob() *idleHostJob {
	j := &idleHostJob{
		Base: job.Base{
			JobType: amboy.JobType{
				Name:    idleHostJobName,
				Version: 0,
			},
		},
	}
	return j
}

func NewIdleHostTerminationJob(env evergreen.Environment, id string) amboy.Job {
	j := makeIdleHostJob()
	j.env = env
	j.SetID(fmt.Sprintf("%s.%s", idleHostJobName, id))
	return j
}

func (j *idleHostJob) Run(ctx context.Context) {
	defer j.MarkComplete()

	if j.env == nil {
		j.env = evergreen.GetEnvironment()
	}

	if j.HasErrors() {
		return
	}

	// Each DistroID's idleHosts are sorted from oldest to newest CreationTime.
	distroHosts, err := host.IdleEphemeralGroupedByDistroID(ctx, j.env)
	if err != nil {
		j.AddError(errors.Wrap(err, "finding idle ephemeral hosts grouped by distro ID"))
		return
	}

	distroIDsToFind := make([]string, 0, len(distroHosts))
	for _, info := range distroHosts {
		distroIDsToFind = append(distroIDsToFind, info.DistroID)
	}
	distrosFound, err := distro.Find(ctx, distro.ByIds(distroIDsToFind))
	if err != nil {
		j.AddError(errors.Wrapf(err, "finding distros"))
		return
	}

	if len(distroIDsToFind) > len(distrosFound) {
		distroIDsFound := make([]string, 0, len(distrosFound))
		for _, d := range distrosFound {
			distroIDsFound = append(distroIDsFound, d.Id)
		}
		missingDistroIDs := utility.GetSetDifference(distroIDsToFind, distroIDsFound)
		hosts, err := host.Find(ctx, host.ByDistroIDs(missingDistroIDs...))
		if err != nil {
			j.AddError(errors.Wrapf(err, "finding hosts in missing distros: %s", strings.Join(missingDistroIDs, ", ")))
			return
		}
		for _, h := range hosts {
			j.AddError(errors.Wrapf(h.SetDecommissioned(ctx, evergreen.User, false, "host's distro not found"), "could not set host '%s' as decommissioned", h.Id))
		}

		if j.HasErrors() {
			return
		}
	}

	distrosMap := make(map[string]distro.Distro, len(distrosFound))
	for i := range distrosFound {
		d := distrosFound[i]
		distrosMap[d.Id] = d
	}

	schedulerConfig := evergreen.SchedulerConfig{}
	if err := schedulerConfig.Get(ctx); err != nil {
		j.AddError(errors.Wrap(err, "getting scheduler config"))
		return
	}

	for _, info := range distroHosts {
		minimumHostsForDistro := distrosMap[info.DistroID].HostAllocatorSettings.MinimumHosts
		minNumHostsToEvaluate := getMinNumHostsToEvaluate(info, minimumHostsForDistro)

		currentDistro := distrosMap[info.DistroID]
		hostsToEvaluateForTermination := make([]host.Host, 0, minNumHostsToEvaluate)
		for i := 0; i < len(info.IdleHosts); i++ {
			if len(hostsToEvaluateForTermination) >= minNumHostsToEvaluate {
				// If we've reached the number that we need to terminate, only terminate hosts with outdated AMIs.
				if !hostHasOutdatedAMI(info.IdleHosts[i], currentDistro) {
					continue
				}
			}
			hostsToEvaluateForTermination = append(hostsToEvaluateForTermination, info.IdleHosts[i])
			j.AddError(j.checkAndTerminateHost(ctx, schedulerConfig, &info.IdleHosts[i], currentDistro))
		}
	}
}

func getMinNumHostsToEvaluate(info host.IdleHostsByDistroID, minimumHosts int) int {
	totalRunningHosts := info.RunningHostsCount
	numIdleHosts := len(info.IdleHosts)

	maxHostsToTerminate := totalRunningHosts - minimumHosts
	if maxHostsToTerminate <= 0 {
		// Even if we're at or below minimum hosts, we should still continue to check hosts with outdated AMIs.
		return 0
	}
	if numIdleHosts > maxHostsToTerminate {
		return maxHostsToTerminate
	}
	return numIdleHosts
}

func (j *idleHostJob) checkAndTerminateHost(ctx context.Context, schedulerConfig evergreen.SchedulerConfig, h *host.Host, d distro.Distro) error {
	exitEarly, err := checkTerminationExemptions(ctx, h, j.env, j.Type().Name, j.ID())
	if exitEarly {
		return err
	}

	idleInfo, err := j.getIdleInfo(ctx, h, &d, schedulerConfig)
	if err != nil {
		return errors.Wrap(err, "getting information on idle host")
	}

	if terminateReason := idleInfo.getTerminationReason(); terminateReason != "" {
		j.Terminated++
		j.TerminatedHosts = append(j.TerminatedHosts, h.Id)
		terminationJob := NewHostTerminationJob(j.env, h, HostTerminationOptions{TerminationReason: terminateReason})
		return EnqueueTerminateHostJob(ctx, j.env, terminationJob)
	}

	return nil
}

type hostIdleInfo struct {
	timeSinceLastCommunication          time.Duration
	idleTime                            time.Duration
	idleThreshold                       time.Duration
	timeSinceTaskGroupTeardownStartTime time.Duration
	isRunningSingleHostTaskGroup        bool
	isRunningTearDownTaskGroup          bool
	hasOutdatedAMI                      bool
}

// getIdleInfo returns information about how long the host has been idle,
// how long it is allowed to be idle, and other special considerations for
// whether the host should be considered idle.
func (j *idleHostJob) getIdleInfo(ctx context.Context, h *host.Host, d *distro.Distro, schedulerConfig evergreen.SchedulerConfig) (hostIdleInfo, error) {
	idleThreshold := d.HostAllocatorSettings.AcceptableHostIdleTime
	if idleThreshold == 0 {
		idleThreshold = time.Duration(schedulerConfig.AcceptableHostIdleTimeSeconds) * time.Second
	}

	// Allow additional idle time for single host task groups in case it is
	// slightly slow getting to the next task in the task group. Disrupting a
	// single host task group breaks continuity and requires restarting the
	// entire task group from the start, which is undesirable.
	const singleHostTaskGroupIdleCutoff = 5 * time.Minute
	isRunningSingleHostTaskGroup, err := isAssignedSingleHostTaskGroup(ctx, h)
	if err != nil {
		return hostIdleInfo{}, errors.Wrap(err, "checking if host is running single host task group")
	}

	if isRunningSingleHostTaskGroup {
		idleThreshold = singleHostTaskGroupIdleCutoff
	} else if h.RunningTaskGroup != "" {
		idleThreshold = idleThreshold * 2
	}

	return hostIdleInfo{
		idleTime:                            h.IdleTime(),
		timeSinceLastCommunication:          h.GetElapsedCommunicationTime(),
		idleThreshold:                       idleThreshold,
		isRunningSingleHostTaskGroup:        isRunningSingleHostTaskGroup,
		isRunningTearDownTaskGroup:          h.IsTearingDown(),
		timeSinceTaskGroupTeardownStartTime: time.Since(h.TaskGroupTeardownStartTime),
		hasOutdatedAMI:                      hostHasOutdatedAMI(*h, *d),
	}, nil
}

// isAssignedSingleHostTaskGroup returns whether the host is assigned to run a
// single host task group.
func isAssignedSingleHostTaskGroup(ctx context.Context, h *host.Host) (bool, error) {
	if h.RunningTaskGroup != "" && h.RunningTask != "" {
		runningTask, err := task.FindOneIdAndExecution(ctx, h.RunningTask, h.RunningTaskExecution)
		if err != nil {
			return false, errors.Wrapf(err, "finding host's running task '%s' execution %d", h.RunningTask, h.RunningTaskExecution)
		}
		if runningTask == nil {
			return false, errors.Errorf("host's running task '%s' execution %d not found", h.RunningTask, h.RunningTaskExecution)
		}
		return runningTask.IsPartOfSingleHostTaskGroup(), nil
	}

	if h.LastGroup != "" && h.RunningTask == "" {
		// Check if the host just finished a successful single host task group
		// task but hasn't gotten to the next one yet (if any). Assume that it
		// will continue onto the next task group task ASAP, so it's still
		// assigned the task group.
		prevTask, err := task.FindOneId(ctx, h.LastTask)
		if err != nil {
			return false, errors.Wrapf(err, "finding host's last task group task '%s'", h.LastTask)
		}
		if prevTask == nil {
			return false, errors.Errorf("host's last task group task '%s' not found", h.LastTask)
		}
		return prevTask.IsPartOfSingleHostTaskGroup() && prevTask.Status == evergreen.TaskSucceeded, nil
	}

	return false, nil
}

// getTerminationReason determines the reason why a host should be terminated.
// It returns a non-empty string if the host should be terminated for idleness
// or other reasons.
func (i hostIdleInfo) getTerminationReason() string {
	if i.hasOutdatedAMI && i.idleTime > 0 && !i.isRunningSingleHostTaskGroup {
		// Since tasks created after the AMI is updated should only run on new
		// hosts, terminate outdated hosts that have been idle for any amount of
		// time to ensure that behavior is being respected. Only do this once
		// the host is actually done with its current task or single host task
		// group, because it won't be able to terminate a host that is currently
		// busy.
		return "host has an outdated AMI"
	}
	if i.timeSinceLastCommunication >= i.idleThreshold && !i.isRunningTearDownTaskGroup {
		return fmt.Sprintf("host is idle or unreachable, communication time %s is over threshold time %s", i.timeSinceLastCommunication, i.idleThreshold)
	}
	// idleTime will be zero for tasks running their teardown group, because heartbeats are not sent during teardown,
	// which is why we need to check if idle time is nonzero.
	if i.idleTime > 0 && i.idleTime >= i.idleThreshold {
		return fmt.Sprintf("host is idle or unreachable, idle time %s is over threshold time %s", i.idleTime, i.idleThreshold)
	}
	if i.timeSinceTaskGroupTeardownStartTime > evergreen.MaxTeardownGroupThreshold && i.isRunningTearDownTaskGroup {
		return fmt.Sprintf("time since the host's task group teardown start time %s has exceeded the maximum teardown threshold %s", i.timeSinceTaskGroupTeardownStartTime, evergreen.MaxTeardownGroupThreshold)
	}

	return ""
}

// checkTerminationExemptions checks if some conditions apply where we shouldn't terminate an idle host,
// and returns true if some exemption applies.
func checkTerminationExemptions(ctx context.Context, h *host.Host, env evergreen.Environment, jobType string, jid string) (bool, error) {
	if !h.IsEphemeral() {
		grip.Notice(message.Fields{
			"job":      jid,
			"host_id":  h.Id,
			"job_type": jobType,
			"status":   h.Status,
			"provider": h.Distro.Provider,
			"message":  "host termination for a non-ephemeral distro",
			"cause":    "programmer error",
		})
		return true, errors.Errorf("attempted to terminate non-ephemeral host '%s'", h.Id)
	}

	// ask the host how long it has been idle
	idleTime := h.IdleTime()

	// if the communication time is > 10 mins then there may not be an agent on the host.
	communicationTime := h.GetElapsedCommunicationTime()

	if h.IsWaitingForAgent() && (communicationTime < idleWaitingForAgentCutoff || idleTime < idleWaitingForAgentCutoff) {
		grip.Notice(message.Fields{
			"op":                jobType,
			"id":                jid,
			"message":           "not flagging idle host, waiting for an agent",
			"host_id":           h.Id,
			"distro":            h.Distro.Id,
			"idle":              idleTime.String(),
			"last_communicated": communicationTime.String(),
		})
		return true, nil
	}

	// get a cloud manager for the host
	mgrOpts, err := cloud.GetManagerOptions(h.Distro)
	if err != nil {
		return true, errors.Wrapf(err, "getting cloud manager options for host '%s'", h.Id)
	}
	manager, err := cloud.GetManager(ctx, env, mgrOpts)
	if err != nil {
		return true, errors.Wrapf(err, "getting cloud manager for host '%s'", h.Id)
	}

	// ask how long until the next payment for the host
	tilNextPayment := manager.TimeTilNextPayment(h)

	if tilNextPayment > maxTimeTilNextPayment {
		return true, nil
	}

	return false, nil
}

func hostHasOutdatedAMI(h host.Host, d distro.Distro) bool {
	return h.GetAMI() != d.GetDefaultAMI()
}
