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
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/git-appraise/repository"
	"github.com/google/git-appraise/review"
	"github.com/google/git-appraise/review/ci"
	github "github.com/google/go-github/github"
)

const (
	maxRetryAttempts = 100
)

var (
	// ErrInvalidRemoteRepo is returned when a given github repo is missing
	// required information.
	ErrInvalidRemoteRepo = errors.New("github repo requires name and owner login")
)

// Utilities for reading all of the pull request data for a specific repository.

// Can be stubbed out in testing; satisfied by github.Client.Repositories
type repositoriesService interface {
	ListStatuses(ctx context.Context, owner, repo, ref string, opt *github.ListOptions) ([]*github.RepoStatus, *github.Response, error)
}

type pullRequestsService interface {
	List(ctx context.Context, owner string, repo string, opt *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error)
	ListComments(ctx context.Context, owner string, repo string, number int, opt *github.PullRequestListCommentsOptions) ([]*github.PullRequestComment, *github.Response, error)
}

type issuesService interface {
	ListComments(ctx context.Context, owner string, repo string, number int, opt *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error)
}

type retryableRequest func() (*github.Response, error)

func executeRequest(request retryableRequest) error {
	for i := 0; i < maxRetryAttempts; i++ {
		resp, err := request()
		if err == nil || resp.StatusCode != http.StatusForbidden || resp.Rate.Remaining != 0 {
			return err
		}
		waitDuration := resp.Rate.Reset.Sub(time.Now())
		log.Printf("Ran out of github API requests; sleeping %v (until %v)",
			waitDuration,
			resp.Rate.Reset.Time)
		time.Sleep(waitDuration)
	}
	return fmt.Errorf("Exceeded the maximum of %d retry attempts", maxRetryAttempts)
}

// A retryableListRequest is a procedure that executes a list request in a way that is safe to retry.
//
// The contract for such a procedure is that it performs *exactly* one of the following:
//  1. Returns an error
// or
//  2. Captures the returned results in some internal state and returns a response with the LastPage field set.
type retryableListRequest func(github.ListOptions) (*github.Response, error)

// executeListRequest takes a retryableListRequest, and runs it for every page of
// results returned by the GitHub API.
func executeListRequest(request retryableListRequest) error {
	for page, maxPage := 1, 1; page <= maxPage; page++ {
		listOpts := github.ListOptions{
			Page:    page,
			PerPage: 100, // The maximum number of results per page
		}
		err := executeRequest(func() (*github.Response, error) {
			resp, err := request(listOpts)
			if err == nil {
				maxPage = resp.LastPage
			}
			return resp, err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// GetAllStatuses iterates through all of the head commits in the remote
// repository, reads their statuses from Github, and returns the git-appraise equivalents.
//
// Errors processing individual channels will be passed through the supplied
// error channel; errors that prevent all processing will be returned directly.
func GetAllStatuses(remoteUser, remoteRepo string, client *github.Client, errOutput chan<- error) (map[string][]ci.Report, error) {
	if remoteUser == "" || remoteRepo == "" {
		return nil, ErrInvalidRemoteRepo
	}
	commits, err := iterateRemoteCommits(remoteUser, remoteRepo, client)
	if err != nil {
		return nil, err
	}

	return fetchStatuses(commits, remoteUser, remoteRepo, client.Repositories, errOutput)
}

// iterateRemoteCommits returns a slice of the head commits for every ref in the remote repo.
func iterateRemoteCommits(remoteUser, remoteRepo string, client *github.Client) ([]string, error) {
	var remoteCommits []string
	err := executeListRequest(func(listOpts github.ListOptions) (*github.Response, error) {
		opts := &github.ReferenceListOptions{
			ListOptions: listOpts,
		}
		refs, response, err := client.Git.ListRefs(context.TODO(), remoteUser, remoteRepo, opts)
		if err == nil {
			for _, ref := range refs {
				remoteCommits = append(remoteCommits, *ref.Object.SHA)
			}
		}
		return response, err
	})
	if err != nil {
		return nil, err
	}
	return remoteCommits, nil
}

func fetchReportsForCommit(commitSHA, remoteUser, remoteRepo string, repoService repositoriesService, errOutput chan<- error) ([]ci.Report, error) {
	var reports []ci.Report
	err := executeListRequest(func(listOpts github.ListOptions) (*github.Response, error) {
		statuses, resp, err := repoService.ListStatuses(context.TODO(), remoteUser, remoteRepo, commitSHA, &listOpts)
		if err == nil {
			for _, status := range statuses {
				report, err := ConvertStatus(status)
				if err != nil {
					errOutput <- err
				} else {
					reports = append(reports, *report)
				}
			}
		}
		return resp, err
	})
	if err != nil {
		return nil, err
	}
	return reports, nil
}

func fetchStatuses(commits []string, remoteUser, remoteRepo string, repoService repositoriesService, errOutput chan<- error) (map[string][]ci.Report, error) {
	reportsByCommitHash := make(map[string][]ci.Report)
	for _, commitSHA := range commits {
		reports, err := fetchReportsForCommit(commitSHA, remoteUser, remoteRepo, repoService, errOutput)
		if err != nil {
			return nil, err
		}
		reportsByCommitHash[commitSHA] = reports
	}
	return reportsByCommitHash, nil
}

// GetAllPullRequests reads all of the pull requests from the given repository.
// It returns successful conversions and encountered errors in a channel.
// Errors processing individual channels will be passed through the supplied
// error channel; errors that prevent all processing will be returned directly.
func GetAllPullRequests(local repository.Repo, remoteUser, remoteRepo string, client *github.Client, errOutput chan<- error) ([]review.Review, error) {
	if remoteUser == "" || remoteRepo == "" {
		return nil, ErrInvalidRemoteRepo
	}

	prs, err := fetchPullRequests(remoteUser, remoteRepo, client.PullRequests)
	if err != nil {
		return nil, err
	}
	var output []review.Review
	for _, pr := range prs {
		issueComments, diffComments, err := fetchComments(pr, remoteUser, remoteRepo, client.PullRequests, client.Issues)
		if err != nil {
			errOutput <- err
		} else {
			review, err := ConvertPullRequestToReview(pr, issueComments, diffComments, local)
			if err != nil {
				errOutput <- err
			} else {
				output = append(output, *review)
			}
		}
	}
	return output, nil
}

func fetchPullRequests(remoteUser, remoteRepo string, prs pullRequestsService) ([]*github.PullRequest, error) {
	var results []*github.PullRequest
	err := executeListRequest(func(listOpts github.ListOptions) (*github.Response, error) {
		opts := &github.PullRequestListOptions{
			State:       "all",
			ListOptions: listOpts,
		}
		pullRequests, response, err := prs.List(context.TODO(), remoteUser, remoteRepo, opts)
		if err == nil {
			results = append(results, pullRequests...)
		}
		return response, err
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// fetchComments fetches all of the comments for each issue it gets and then converts them.
func fetchComments(pr *github.PullRequest, remoteUser, remoteRepo string, prs pullRequestsService, is issuesService) ([]*github.IssueComment, []*github.PullRequestComment, error) {
	var issueComments []*github.IssueComment
	err := executeListRequest(func(listOpts github.ListOptions) (*github.Response, error) {
		listOptions := &github.IssueListCommentsOptions{
			ListOptions: listOpts,
		}
		cs, resp, err := is.ListComments(context.TODO(), remoteUser, remoteRepo, *pr.Number, listOptions)
		if err == nil {
			issueComments = append(issueComments, cs...)
		}
		return resp, err
	})
	if err != nil {
		return nil, nil, err
	}
	var diffComments []*github.PullRequestComment
	err = executeListRequest(func(listOpts github.ListOptions) (*github.Response, error) {
		listOptions := &github.PullRequestListCommentsOptions{
			ListOptions: listOpts,
		}
		cs, resp, err := prs.ListComments(context.TODO(), remoteUser, remoteRepo, *pr.Number, listOptions)
		if err == nil {
			diffComments = append(diffComments, cs...)
		}
		return resp, err
	})
	if err != nil {
		return nil, nil, err
	}
	return issueComments, diffComments, nil
}
