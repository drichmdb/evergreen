package repotracker

import (
	"context"

	"github.com/evergreen-ci/evergreen/model"
	"github.com/pkg/errors"
)

// MockRepoPoller is a utility for testing the repotracker using a dummy
// project
type mockRepoPoller struct {
	parserProject *model.ParserProject
	revisions     []model.Revision

	ConfigGets uint
	nextError  error
	badDistro  string
}

// Creates a new MockRepo poller with the given project settings
func NewMockRepoPoller(mockProject *model.ParserProject, mockRevisions []model.Revision) *mockRepoPoller {
	return &mockRepoPoller{
		parserProject: mockProject,
		revisions:     mockRevisions,
	}
}

func (d *mockRepoPoller) setNextError(err error) {
	d.nextError = err
}

func (d *mockRepoPoller) addBadDistro(distro string) {
	d.badDistro = distro
}

func (d *mockRepoPoller) clearError() (err error) {
	err, d.nextError = d.nextError, nil
	return err
}

func (d *mockRepoPoller) GetChangedFiles(_ context.Context, commitRevision string) ([]string, error) {
	return nil, nil
}

func (d *mockRepoPoller) GetRemoteConfig(_ context.Context, revision string) (model.ProjectInfo, error) {
	d.ConfigGets++
	if d.nextError != nil {
		return model.ProjectInfo{}, d.clearError()
	}
	if d.badDistro != "" {
		// change the target distros if we've called addBadDistro, creating a validation warning
		d.parserProject.BuildVariants[0].RunOn = append(d.parserProject.BuildVariants[0].RunOn, d.badDistro)
		d.badDistro = ""
	}

	p, err := model.TranslateProject(d.parserProject)
	if err != nil {
		return model.ProjectInfo{}, errors.Wrapf(err, model.TranslateProjectError)
	}
	return model.ProjectInfo{
		Project:             p,
		IntermediateProject: d.parserProject,
		Config:              nil,
	}, nil
}

func (d *mockRepoPoller) GetRevisionsSince(ctx context.Context, revision string, maxRevisionsToSearch int) ([]model.Revision, error) {
	if d.nextError != nil {
		return nil, d.clearError()
	}
	return d.revisions, nil
}

func (d *mockRepoPoller) GetRecentRevisions(maxRevisionsToSearch int) ([]model.Revision, error) {
	if d.nextError != nil {
		return nil, d.clearError()
	}
	return d.revisions, nil
}
