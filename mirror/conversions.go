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
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/git-appraise/repository"
	"github.com/google/git-appraise/review"
	"github.com/google/git-appraise/review/ci"
	"github.com/google/git-appraise/review/comment"
	"github.com/google/git-appraise/review/request"
	github "github.com/google/go-github/github"
)

var (
	// ErrNoTimestamp is exactly what it sounds like.
	ErrNoTimestamp = errors.New("Github status contained no timestamp")
	// ErrInvalidState is returned when a github repository status has an
	// invalid state.
	ErrInvalidState = errors.New(`Github status state was not "success", "failure", "error", "pending", or null`)
	// ErrInsufficientInfo is returned when not enough information is given
	// to perform a valid conversion.
	ErrInsufficientInfo = errors.New("insufficient data for meaningful conversion")
)

// ConvertTime converts a Time instance into the serialized string used in the git-appraise JSON formats.
func ConvertTime(t time.Time) string {
	return fmt.Sprintf("%10d", t.Unix())
}

// ConvertStatus converts a commit status fetched from the GitHub API into a CI report.
func ConvertStatus(repoStatus github.RepoStatus) (*ci.Report, error) {
	result := ci.Report{}
	if repoStatus.UpdatedAt != nil {
		result.Timestamp = ConvertTime(*repoStatus.UpdatedAt)
	} else if repoStatus.CreatedAt != nil {
		result.Timestamp = ConvertTime(*repoStatus.CreatedAt)
	} else {
		return nil, ErrNoTimestamp
	}

	if repoStatus.State != nil {
		if *repoStatus.State == "success" {
			result.Status = ci.StatusSuccess
		} else if *repoStatus.State == "failure" || *repoStatus.State == "error" {
			result.Status = ci.StatusFailure
		} else if *repoStatus.State != "pending" {
			return nil, ErrInvalidState
		}
	}

	if repoStatus.TargetURL != nil {
		result.URL = *repoStatus.TargetURL
	}

	if repoStatus.Context != nil {
		result.Agent = *repoStatus.Context
	}
	return &result, nil
}

// ConvertPullRequest converts a pull request fetched from the GitHub API into a review request.
func ConvertPullRequest(pr github.PullRequest) (*request.Request, error) {
	if pr.Number == nil || pr.User.Login == nil ||
		pr.Base == nil || pr.Base.Ref == nil || pr.Base.SHA == nil ||
		(pr.CreatedAt == nil && pr.UpdatedAt == nil) {
		return nil, ErrInsufficientInfo
	}

	var timestamp string
	if pr.UpdatedAt != nil {
		timestamp = ConvertTime(*pr.UpdatedAt)
	} else {
		timestamp = ConvertTime(*pr.CreatedAt)
	}

	var targetRef string
	if strings.HasPrefix(*pr.Base.Ref, "refs/heads") {
		targetRef = *pr.Base.Ref
	} else {
		targetRef = fmt.Sprintf("refs/heads/%s", *pr.Base.Ref)
	}

	var description string
	if pr.Title != nil {
		description = *pr.Title
	}
	if pr.Body != nil && *pr.Body != "" {
		description += "\n\n" + *pr.Body
	}

	r := request.Request{
		Timestamp:   timestamp,
		ReviewRef:   fmt.Sprintf("refs/pull/%d/head", *pr.Number),
		TargetRef:   targetRef,
		Requester:   *pr.User.Login,
		Description: description,
		BaseCommit:  *pr.Base.SHA,
	}
	return &r, nil
}

// ConvertIssueComment converts a comment on the issue associated with a pull request into a git-appraise review comment.
func ConvertIssueComment(issueComment github.IssueComment) (*comment.Comment, error) {
	if issueComment.User == nil || issueComment.User.Login == nil || issueComment.Body == nil ||
		(issueComment.UpdatedAt == nil && issueComment.CreatedAt == nil) {
		return nil, ErrInsufficientInfo
	}

	var timestamp string
	if issueComment.UpdatedAt != nil {
		timestamp = ConvertTime(*issueComment.UpdatedAt)
	}
	if issueComment.CreatedAt != nil {
		timestamp = ConvertTime(*issueComment.CreatedAt)
	}

	c := comment.Comment{
		Timestamp:   timestamp,
		Author:      *issueComment.User.Login,
		Description: *issueComment.Body,
	}
	return &c, nil
}

// ConvertDiffComment converts a comment on the diff associated with a pull request into a git-appraise review comment.
func ConvertDiffComment(diffComment github.PullRequestComment) (*comment.Comment, error) {
	if diffComment.User == nil || diffComment.User.Login == nil || diffComment.Body == nil ||
		(diffComment.UpdatedAt == nil && diffComment.CreatedAt == nil) ||
		diffComment.OriginalCommitID == nil {
		return nil, ErrInsufficientInfo
	}

	var timestamp string
	if diffComment.UpdatedAt != nil {
		timestamp = ConvertTime(*diffComment.UpdatedAt)
	}
	if diffComment.CreatedAt != nil {
		timestamp = ConvertTime(*diffComment.CreatedAt)
	}

	c := comment.Comment{
		Timestamp:   timestamp,
		Author:      *diffComment.User.Login,
		Description: *diffComment.Body,
		Location: &comment.Location{
			Commit: *diffComment.OriginalCommitID,
		},
	}
	if diffComment.Path != nil {
		c.Location.Path = *diffComment.Path
		if diffComment.DiffHunk != nil {
			startLine, err := commentStartLine(diffComment)
			if err != nil {
				return nil, err
			}
			c.Location.Range = &comment.Range{
				StartLine: startLine,
			}
		}
	}
	return &c, nil
}

// ConvertPullRequestToReview converts a pull request from the GitHub API into a git-appraise review.
//
// Since the GitHub API returns pull request data in three different places (the PullRequest
// object, the list of comments on the corresponding issue, and the list of diff comments),
// all three must be supplied.
//
// This method requires a local clone of the repository in order to compute the locations of
// the different commits in the review.
func ConvertPullRequestToReview(pr github.PullRequest, issueComments []github.IssueComment, diffComments []github.PullRequestComment, repo repository.Repo) (*review.Review, error) {
	request, err := ConvertPullRequest(pr)
	if err != nil {
		return nil, err
	}
	revision, err := computeReviewStartingCommit(pr, repo)
	if err != nil {
		return nil, err
	}

	var comments []review.CommentThread
	for _, issueComment := range issueComments {
		c, err := ConvertIssueComment(issueComment)
		if err != nil {
			return nil, err
		}
		hash, err := c.Hash()
		if err != nil {
			return nil, err
		}
		comments = append(comments, review.CommentThread{
			Hash:    hash,
			Comment: *c,
		})
	}
	for _, diffComment := range diffComments {
		c, err := ConvertDiffComment(diffComment)
		if err != nil {
			return nil, err
		}
		hash, err := c.Hash()
		if err != nil {
			return nil, err
		}
		comments = append(comments, review.CommentThread{
			Hash:    hash,
			Comment: *c,
		})
	}
	r := review.Review{
		Summary: &review.Summary{
			Repo:     repo,
			Revision: revision,
			Request:  *request,
			Comments: comments,
		},
	}

	return &r, nil
}

// commentStartLine takes a PullRequestComment and returns the comment's start line.
func commentStartLine(diffComment github.PullRequestComment) (uint32, error) {
	// This takes some contortions to figure out. The diffComment has a "position"
	// field, but that is not the position of the comment. Instead, that is the
	// position of the comment within the diff. Furthermore, this diff is a unified diff,
	// so that position number includes lines which the diff removes. On the flip side,
	// the diff included is only the portion of the diff that precedes the comment, so
	// we don't actually need the position field at all.
	//
	// As such, to get the actual line number we have to:
	// 1. Parse the diff to find out at which line it starts.
	// 2. Split the diff by lines.
	// 3. Remove all of the lines that start with "-" (indicating a removal).
	// 4. Count the number of lines left after #3.
	//
	// Finally, we add the results from #1 and #4 to get the actual start line.
	diffLines := strings.Split(*diffComment.DiffHunk, "\n")
	if len(diffLines) < 2 {
		// This shouldn't happen; it means we recieved an invalid hunk from GitHub.
		return 0, fmt.Errorf("Insufficient comment diff-hunk: %q", *diffComment.DiffHunk)
	}

	// The first line of the hunk should have the following format:
	//  @@ -lhs-start-line[,lhs-end-line] +rhs-start-line[,rhs-end-line] @@...
	// ... what we care about is the rhs-start-line.
	hunkStartPattern := regexp.MustCompile("@@ -([[:digit:]]+)(,[[:digit:]]+)? \\+([[:digit:]]+)(,[[:digit:]]+)? @@")
	hunkStartParts := hunkStartPattern.FindStringSubmatch(diffLines[0])
	if len(hunkStartParts) < 4 {
		// This shouldn't happen; it means the start of the hunk is malformed
		return 0, fmt.Errorf("Mallformed diff-hunk first line: %q", diffLines[0])
	}
	rhsStartLineString := hunkStartParts[3]
	diffPosition, err := strconv.Atoi(rhsStartLineString)
	if err != nil {
		return 0, err
	}
	if len(diffLines) > 1 {
		for _, line := range diffLines[1:] {
			if !strings.HasPrefix(line, "-") {
				diffPosition = diffPosition + 1
			}
		}
	}
	return uint32(diffPosition), nil
}

// computeReviewStartingCommit computes the first commit in the review.
func computeReviewStartingCommit(pr github.PullRequest, repo repository.Repo) (string, error) {
	if pr.Base == nil || pr.Base.SHA == nil ||
		pr.Head == nil || pr.Head.SHA == nil {
		return "", ErrInsufficientInfo
	}

	prCommits, err := repo.ListCommitsBetween(*pr.Base.SHA, *pr.Head.SHA)
	if err != nil {
		return "", err
	}
	if len(prCommits) == 0 {
		return *pr.Head.SHA, nil
	}
	return prCommits[0], nil
}
