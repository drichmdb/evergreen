package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/mock"
	"github.com/evergreen-ci/evergreen/model/build"
	"github.com/evergreen-ci/evergreen/model/task"
	modelutil "github.com/evergreen-ci/evergreen/model/testutil"
	"github.com/evergreen-ci/evergreen/model/user"
	"github.com/evergreen-ci/evergreen/testutil"
	"github.com/evergreen-ci/gimlet"
	"github.com/stretchr/testify/require"

	. "github.com/smartystreets/goconvey/convey"
)

var buildTestConfig = testutil.TestConfig()

func TestGetBuildInfo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := &mock.Environment{}
	require.NoError(t, env.Configure(ctx))
	router, err := newTestUIRouter(ctx, env)
	require.NoError(t, err, "error setting up router")

	Convey("When finding info on a particular build", t, func() {
		require.NoError(t, db.ClearCollections(build.Collection, task.Collection),
			"Error clearing '%v' collection", build.Collection)

		buildId := "my-build"
		versionId := "my-version"
		projectName := "mci-test"

		err := modelutil.CreateTestLocalConfig(ctx, buildTestConfig, "mci-test", "")
		So(err, ShouldBeNil)

		err = modelutil.CreateTestLocalConfig(ctx, buildTestConfig, "render", "")
		So(err, ShouldBeNil)

		err = modelutil.CreateTestLocalConfig(ctx, buildTestConfig, "project_test", "")
		So(err, ShouldBeNil)

		task := task.Task{
			Id:          "some-task-id",
			DisplayName: "some-task-name",
			Status:      "success",
			TimeTaken:   100 * time.Millisecond,
			BuildId:     buildId,
		}
		So(task.Insert(t.Context()), ShouldBeNil)
		build := &build.Build{
			Id:                  buildId,
			CreateTime:          time.Now().Add(-20 * time.Minute),
			StartTime:           time.Now().Add(-10 * time.Minute),
			FinishTime:          time.Now().Add(-5 * time.Second),
			Version:             versionId,
			Project:             projectName,
			Revision:            fmt.Sprintf("%x", rand.Intn(10)),
			BuildVariant:        "some-build-variant",
			Status:              "success",
			Activated:           true,
			ActivatedTime:       time.Now().Add(-15 * time.Minute),
			RevisionOrderNumber: rand.Intn(10),
			Tasks:               []build.TaskCache{{Id: task.Id}},
			TimeTaken:           10 * time.Minute,
			DisplayName:         "My build",
			Requester:           evergreen.RepotrackerVersionRequester,
		}
		So(build.Insert(t.Context()), ShouldBeNil)

		url := "/rest/v1/builds/" + buildId

		request, err := http.NewRequest("GET", url, nil)
		request = request.WithContext(gimlet.AttachUser(request.Context(), &user.DBUser{Id: "user"}))
		So(err, ShouldBeNil)
		response := httptest.NewRecorder()
		// Need match variables to be set so can call mux.Vars(request)
		// in the actual handler function
		router.ServeHTTP(response, request)
		So(response.Code, ShouldEqual, http.StatusOK)

		Convey("response should match contents of database", func() {
			var jsonBody map[string]any
			err = json.Unmarshal(response.Body.Bytes(), &jsonBody)
			So(err, ShouldBeNil)

			var rawJsonBody map[string]*json.RawMessage
			err = json.Unmarshal(response.Body.Bytes(), &rawJsonBody)
			So(err, ShouldBeNil)

			So(jsonBody["id"], ShouldEqual, build.Id)

			var createTime time.Time
			err = json.Unmarshal(*rawJsonBody["create_time"], &createTime)
			So(err, ShouldBeNil)
			So(createTime, ShouldHappenWithin, TimePrecision, build.CreateTime)

			var startTime time.Time
			err = json.Unmarshal(*rawJsonBody["start_time"], &startTime)
			So(err, ShouldBeNil)
			So(startTime, ShouldHappenWithin, TimePrecision, build.StartTime)

			var finishTime time.Time
			err = json.Unmarshal(*rawJsonBody["finish_time"], &finishTime)
			So(err, ShouldBeNil)
			So(finishTime, ShouldHappenWithin, TimePrecision, build.FinishTime)

			So(jsonBody["version"], ShouldEqual, build.Version)
			So(jsonBody["project"], ShouldEqual, build.Project)
			So(jsonBody["revision"], ShouldEqual, build.Revision)
			So(jsonBody["variant"], ShouldEqual, build.BuildVariant)
			So(jsonBody["status"], ShouldEqual, build.Status)
			So(jsonBody["activated"], ShouldEqual, build.Activated)

			var activatedTime time.Time
			err = json.Unmarshal(*rawJsonBody["activated_time"], &activatedTime)
			So(err, ShouldBeNil)
			So(activatedTime, ShouldHappenWithin, TimePrecision, build.ActivatedTime)

			So(jsonBody["order"], ShouldEqual, build.RevisionOrderNumber)

			_jsonTasks, ok := jsonBody["tasks"]
			So(ok, ShouldBeTrue)
			jsonTasks, ok := _jsonTasks.(map[string]any)
			So(ok, ShouldBeTrue)
			So(len(jsonTasks), ShouldEqual, 1)

			_jsonTask, ok := jsonTasks[task.DisplayName]
			So(ok, ShouldBeTrue)
			jsonTask, ok := _jsonTask.(map[string]any)
			So(ok, ShouldBeTrue)

			So(jsonTask["task_id"], ShouldEqual, task.Id)
			So(jsonTask["status"], ShouldEqual, task.Status)
			So(jsonTask["time_taken"], ShouldEqual, task.TimeTaken)

			So(jsonBody["time_taken"], ShouldEqual, build.TimeTaken)
			So(jsonBody["name"], ShouldEqual, build.DisplayName)
			So(jsonBody["requester"], ShouldEqual, build.Requester)
		})
	})

	Convey("When finding info on a nonexistent build", t, func() {
		buildId := "not-present"

		url := "/rest/v1/builds/" + buildId

		request, err := http.NewRequest("GET", url, nil)
		So(err, ShouldBeNil)
		request = request.WithContext(gimlet.AttachUser(request.Context(), &user.DBUser{Id: "user"}))

		response := httptest.NewRecorder()
		// Need match variables to be set so can call mux.Vars(request)
		// in the actual handler function
		router.ServeHTTP(response, request)

		So(response.Code, ShouldEqual, http.StatusNotFound)

		Convey("response should contain a sensible error message", func() {
			var jsonBody map[string]any
			err = json.Unmarshal(response.Body.Bytes(), &jsonBody)
			So(err, ShouldBeNil)
			So(len(jsonBody["message"].(string)), ShouldBeGreaterThan, 0)
		})
	})
}

func TestGetBuildStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := &mock.Environment{}
	require.NoError(t, env.Configure(ctx))
	router, err := newTestUIRouter(ctx, env)
	require.NoError(t, err, "error setting up router")

	Convey("When finding the status of a particular build", t, func() {
		require.NoError(t, db.ClearCollections(build.Collection, task.Collection), "Error clearing collections")

		buildId := "my-build"
		versionId := "my-version"
		taskId := "some-task-id"

		build := &build.Build{
			Id:           buildId,
			Version:      versionId,
			BuildVariant: "some-build-variant",
			DisplayName:  "Some Build Variant",
			Tasks:        []build.TaskCache{{Id: taskId}},
		}
		So(build.Insert(t.Context()), ShouldBeNil)

		task := task.Task{
			Id:          taskId,
			BuildId:     build.Id,
			DisplayName: "some-task-name",
			Status:      "success",
			TimeTaken:   100 * time.Millisecond,
		}
		So(task.Insert(t.Context()), ShouldBeNil)

		url := "/rest/v1/builds/" + buildId + "/status"

		request, err := http.NewRequest("GET", url, nil)
		So(err, ShouldBeNil)
		request = request.WithContext(gimlet.AttachUser(request.Context(), &user.DBUser{Id: "user"}))

		response := httptest.NewRecorder()
		// Need match variables to be set so can call mux.Vars(request)
		// in the actual handler function
		router.ServeHTTP(response, request)

		So(response.Code, ShouldEqual, http.StatusOK)

		Convey("response should match contents of database", func() {
			var jsonBody map[string]any
			err = json.Unmarshal(response.Body.Bytes(), &jsonBody)
			So(err, ShouldBeNil)

			var rawJsonBody map[string]*json.RawMessage
			err = json.Unmarshal(response.Body.Bytes(), &rawJsonBody)
			So(err, ShouldBeNil)

			So(jsonBody["build_id"], ShouldEqual, build.Id)
			So(jsonBody["build_variant"], ShouldEqual, build.BuildVariant)

			_jsonTasks, ok := jsonBody["tasks"]
			So(ok, ShouldBeTrue)
			jsonTasks, ok := _jsonTasks.(map[string]any)
			So(ok, ShouldBeTrue)
			So(len(jsonTasks), ShouldEqual, 1)

			_jsonTask, ok := jsonTasks[task.DisplayName]
			So(ok, ShouldBeTrue)
			jsonTask, ok := _jsonTask.(map[string]any)
			So(ok, ShouldBeTrue)

			So(jsonTask["task_id"], ShouldEqual, task.Id)
			So(jsonTask["status"], ShouldEqual, task.Status)
			So(jsonTask["time_taken"], ShouldEqual, task.TimeTaken)
		})
	})

	Convey("When finding the status of a nonexistent build", t, func() {
		buildId := "not-present"

		url := "/rest/v1/builds/" + buildId + "status"

		request, err := http.NewRequest("GET", url, nil)
		So(err, ShouldBeNil)
		request = request.WithContext(gimlet.AttachUser(request.Context(), &user.DBUser{Id: "user"}))

		response := httptest.NewRecorder()
		// Need match variables to be set so can call mux.Vars(request)
		// in the actual handler function
		router.ServeHTTP(response, request)

		So(response.Code, ShouldEqual, http.StatusNotFound)

		Convey("response should contain a sensible error message", func() {
			var jsonBody map[string]any
			err = json.Unmarshal(response.Body.Bytes(), &jsonBody)
			So(err, ShouldBeNil)
			So(len(jsonBody["message"].(string)), ShouldBeGreaterThan, 0)
		})
	})
}
