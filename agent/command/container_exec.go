package command

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/evergreen-ci/evergreen/agent/internal"
	"github.com/evergreen-ci/evergreen/agent/internal/client"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/mitchellh/mapstructure"
	"github.com/mongodb/grip"
	"github.com/pkg/errors"
)

func (e *containerExec) Name() string { return "container.exec" }

func containerExecFactory() Command { return &containerExec{} }

type containerExec struct {
	WorkDir    string            `mapstructure:"working_dir" plugin:"expand"`
	Env        map[string]string `mapstructure:"env" plugin:"expand"`
	Command    string            `mapstructure:"command" plugin:"expand"`
	Entrypoint string            `mapstructure:"entrypoint" plugin:"expand"`
	Image      string            `mapstructure:"image" plugin:"expand"`
	Repository string            `mapstructure:"repository" plugin:"expand"`
	base
}

func (e *containerExec) Execute(ctx context.Context,
	comm client.Communicator, logger client.LoggerProducer, conf *internal.TaskConfig) error {
	if err := util.ExpandValues(e, &conf.Expansions); err != nil {
		return errors.Wrap(err, "applying expansions")
	}

	binpath, err := exec.LookPath("podman")
	if err != nil {
		return errors.Wrap(err, "getting podman path")
	}

	args := []string{
		"run",
		"--rm",
		fmt.Sprintf("--volume=%s:/task", conf.WorkDir),
		fmt.Sprintf("--workdir=%s", e.WorkDir),
	}

	for k, v := range e.Env {
		args = append(args, fmt.Sprintf("--env=%s=%s", k, v))
	}

	if e.Entrypoint != "" {
		args = append(args, fmt.Sprintf("--entrypoint=%s", e.Entrypoint))
	}

	fullname := fmt.Sprintf("%s/%s", e.Repository, e.Image)

	args = append(args, fullname)
	args = append(args, e.Command)

	cmd := exec.CommandContext(ctx, binpath, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrap(err, "running container")
	}

	tl := logger.Task()

	tl.Infof(string(output))

	return nil
}

func (e *containerExec) ParseParams(params map[string]any) error {
	conf := &mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		Result:           e,
	}

	decoder, err := mapstructure.NewDecoder(conf)
	if err != nil {
		return errors.Wrap(err, "initializing new decoder")
	}

	if err := decoder.Decode(params); err != nil {
		return errors.Wrap(err, "decoding params")
	}

	catcher := grip.NewSimpleCatcher()

	return catcher.Resolve()
}
