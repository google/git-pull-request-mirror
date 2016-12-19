/*
Copyright 2015 Google Inc. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mirror

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/git-appraise/review/ci"
	github "github.com/google/go-github/github"
)

var (
	stateSuccess             = "success"
	stateFailure             = "failure"
	statusTargetURLFormat    = "ci.example.com/%d"
	statusContext            = "CI Runner"
	pageCount                = 10
	statusSuccessfulResponse = github.Response{
		Response: &http.Response{
			StatusCode: http.StatusOK,
		},
		LastPage: pageCount,
		Rate: github.Rate{
			Remaining: 1,
		},
	}
	statusThrottledResponse = github.Response{
		Response: &http.Response{
			StatusCode: http.StatusForbidden,
		},
		LastPage: pageCount,
		Rate: github.Rate{
			Remaining: 0,
		},
	}
)

type repoServiceResponse struct {
	Results  []github.RepoStatus
	Response github.Response
	Error    error
}

type repoServiceStub struct {
	Index     int
	Responses []repoServiceResponse
}

func (s *repoServiceStub) ListStatuses(owner, repo, ref string, opt *github.ListOptions) ([]*github.RepoStatus, *github.Response, error) {
	if s.Index >= len(s.Responses) {
	}
	r := s.Responses[s.Index]
	s.Index++
	return r.Results, &r.Response, r.Error
}

func TestFetchReports(t *testing.T) {
	var responses []repoServiceResponse
	var expectedReports []ci.Report

	now := time.Now()
	for i := 0; i < pageCount; i++ {
		successURL := fmt.Sprintf(statusTargetURLFormat, i*2)
		successResult := github.RepoStatus{
			CreatedAt: &now,
			State:     &stateSuccess,
			TargetURL: &successURL,
			Context:   &statusContext,
		}
		successReport, err := ConvertStatus(successResult)
		if err != nil {
			t.Fatal(err)
		}
		failureURL := fmt.Sprintf(statusTargetURLFormat, i*2+1)
		failureResult := github.RepoStatus{
			CreatedAt: &now,
			State:     &stateFailure,
			TargetURL: &failureURL,
			Context:   &statusContext,
		}
		failureReport, err := ConvertStatus(failureResult)
		if err != nil {
			t.Fatal(err)
		}
		expectedReports = append(expectedReports, *successReport)
		expectedReports = append(expectedReports, *failureReport)
		responses = append(responses, repoServiceResponse{
			Results: []*github.RepoStatus{
				successResult,
				failureResult,
			},
			Response: statusSuccessfulResponse,
			Error:    nil,
		})
		responses = append(responses, repoServiceResponse{
			Results:  nil,
			Response: statusThrottledResponse,
			Error:    fmt.Errorf("Too many requests, for now"),
		})
	}
	serviceStub := &repoServiceStub{
		Index:     0,
		Responses: responses,
	}

	errOut := make(chan error, 1000)
	resultingReports, err := fetchReportsForCommit("ABCDEF", "user", "repo", serviceStub, errOut)
	if err != nil || len(errOut) > 0 {
		t.Fatal(err, errOut)
	}
	if len(resultingReports) != len(expectedReports) {
		t.Errorf("Unexpected reports: %v vs. %v", resultingReports, expectedReports)
	}
	for i := range resultingReports {
		if resultingReports[i] != expectedReports[i] {
			t.Errorf("Unexpected reports: %v vs. %v", resultingReports, expectedReports)
		}
	}
}
