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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/git-appraise/repository"
	"github.com/google/git-appraise/review"
	"github.com/google/git-appraise/review/ci"
	github "github.com/google/go-github/github"
)

var (
	// Even though these are constants, we define them as variables so we can take their addresses.
	repoOwner        = "example_org"
	repoName         = "example_repo"
	contributorLogin = "helpful_contributor"
)

func TestConvertStatus(t *testing.T) {
	state := "success"
	targetURL := "https://ci.example.com/build"
	context := "ci/example"
	createdAt := time.Now().Truncate(time.Second)
	input := github.RepoStatus{
		State:     &state,
		TargetURL: &targetURL,
		Context:   &context,
		CreatedAt: &createdAt,
	}
	result, err := ConvertStatus(input)
	if err != nil || result == nil {
		t.Fatal(err)
	}
	if result.Status != ci.StatusSuccess {
		t.Errorf("%v != %v", result.Status, ci.StatusSuccess)
	}
	if result.URL != targetURL {
		t.Errorf("%v != %v", result.URL, targetURL)
	}
	if result.Agent != context {
		t.Errorf("%v != %v", result.Agent, context)
	}
	resultTimestamp, err := strconv.ParseInt(result.Timestamp, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	resultTime := time.Unix(resultTimestamp, 0)
	if resultTime != createdAt {
		t.Errorf("%v != %v", resultTime, createdAt)
	}
}

func buildTestPullRequest(testRepo repository.Repo, reqNum int) github.PullRequest {
	reqTime := time.Now().Add(-3 * time.Hour)
	reqTitle := "Bug fixes."
	reqBody := "Fix some bugs."

	baseRef := repository.TestTargetRef
	baseCommitSHA := repository.TestCommitE
	headRef := repository.TestReviewRef
	headCommitSHA := repository.TestCommitG
	return github.PullRequest{
		CreatedAt: &reqTime,
		Body:      &reqBody,
		Title:     &reqTitle,
		Number:    &reqNum,
		Base: &github.PullRequestBranch{
			Ref: &baseRef,
			SHA: &baseCommitSHA,
			Repo: &github.Repository{
				Name: &repoName,
				Owner: &github.User{
					Login: &repoOwner,
				},
			},
		},
		Head: &github.PullRequestBranch{
			Ref: &headRef,
			SHA: &headCommitSHA,
			Repo: &github.Repository{
				Name: &repoName,
				Owner: &github.User{
					Login: &repoOwner,
				},
			},
		},
		User: &github.User{
			Login: &contributorLogin,
		},
	}
}

func TestConvertPullRequest(t *testing.T) {
	testRepo := repository.NewMockRepoForTest()
	reqNum := 4
	pullRef := fmt.Sprintf("refs/pull/%d/head", reqNum)
	pr := buildTestPullRequest(testRepo, reqNum)
	r, err := ConvertPullRequest(pr)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("Unexpected nil request")
	}
	if r.ReviewRef != pullRef || r.TargetRef != *pr.Base.Ref || r.Requester != contributorLogin ||
		!strings.Contains(r.Description, *pr.Title) || !strings.Contains(r.Description, *pr.Body) ||
		r.BaseCommit != *pr.Base.SHA || r.Timestamp != ConvertTime(*pr.CreatedAt) {
		t.Errorf("Unexpected request %v", r)
	}
}

func verifyCommentPresent(r *review.Review, message, author string) bool {
	for _, thread := range r.Comments {
		if thread.Comment.Description == message && thread.Comment.Author == author {
			return true
		}
	}
	return false
}

func verifyCommentPresentAtLine(r *review.Review, message, author string, lineNumber uint32) bool {
	for _, thread := range r.Comments {
		if thread.Comment.Description == message && thread.Comment.Author == author &&
			thread.Comment.Location.Range.StartLine == lineNumber {
			return true
		}
	}
	return false
}

func TestConvertPullRequestToReview(t *testing.T) {
	testRepo := repository.NewMockRepoForTest()
	reqNum := 4
	pr := buildTestPullRequest(testRepo, reqNum)
	now := time.Now()

	issueComment1 := "Please sign our CLA"
	issueTime1 := now.Add(-2 * time.Hour)
	issueComment2 := "Done"
	issueTime2 := now.Add(-1 * time.Hour)
	issueComments := []github.IssueComment{
		github.IssueComment{
			Body: &issueComment1,
			User: &github.User{
				Login: &repoOwner,
			},
			CreatedAt: &issueTime1,
		},
		github.IssueComment{
			Body: &issueComment2,
			User: &github.User{
				Login: &contributorLogin,
			},
			CreatedAt: &issueTime2,
		},
	}

	filePath := "example.go"
	diffHunk := "@@ -4,6 +10,10 @@ func changedMethod() {\n \t// This is an existing line\n \t// This is another existing line\n-\t//This is a removed line\n+\t//This is a new line\n+\t//This is a second new line, with a comment\")"
	var commentLineNumber uint32 = 14
	diffComment1 := "Comment on line 14"
	diffTime1 := now.Add(-2 * time.Hour)
	diffComment2 := "Reply to comment on line 14"
	diffTime2 := now.Add(-2 * time.Hour)
	diffCommit := repository.TestCommitG
	diffComments := []github.PullRequestComment{
		github.PullRequestComment{
			Body:             &diffComment1,
			Path:             &filePath,
			OriginalCommitID: &diffCommit,
			DiffHunk:         &diffHunk,
			User: &github.User{
				Login: &repoOwner,
			},
			CreatedAt: &diffTime1,
		},
		github.PullRequestComment{
			Body:             &diffComment2,
			Path:             &filePath,
			OriginalCommitID: &diffCommit,
			DiffHunk:         &diffHunk,
			User: &github.User{
				Login: &contributorLogin,
			},
			CreatedAt: &diffTime2,
		},
	}

	r, err := ConvertPullRequestToReview(pr, issueComments, diffComments, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("Unexpected nil review")
	}
	reviewJSON, err := r.GetJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !verifyCommentPresent(r, issueComment1, repoOwner) ||
		!verifyCommentPresent(r, issueComment2, contributorLogin) {
		t.Fatal("Missing expected top-level comments")
	}
	if !verifyCommentPresentAtLine(r, diffComment1, repoOwner, commentLineNumber) ||
		!verifyCommentPresentAtLine(r, diffComment2, contributorLogin, commentLineNumber) {
		t.Errorf("Missing expected line comments: %s", reviewJSON)
	}
}
