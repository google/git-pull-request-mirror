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
	"encoding/json"
	"fmt"
	"github.com/google/git-appraise/repository"
	"github.com/google/git-appraise/review"
	"github.com/google/git-appraise/review/ci"
	"github.com/google/git-appraise/review/comment"
	"github.com/google/git-appraise/review/request"
)

// WriteNewReports takes a list of CI reports read from GitHub, and writes to the repo any that are new.
//
// The passed in logChan variable is used as our intermediary for logging, and allows us to
// use the same logic for logging messages in either our CLI or our App Engine apps, even though
// the two have different logging frameworks.
func WriteNewReports(reportsMap map[string][]ci.Report, repo repository.Repo, logChan chan<- string) error {
	for commit, commitReports := range reportsMap {
		existingReports := ci.ParseAllValid(repo.GetNotes(ci.Ref, commit))
		for _, report := range commitReports {
			bytes, err := json.Marshal(report)
			note := repository.Note(bytes)
			if err != nil {
				return err
			}
			missing := true
			for _, existing := range existingReports {
				if existing == report {
					missing = false
				}
			}
			if missing {
				logChan <- fmt.Sprintf("Found a new report for %.12s: %q", commit, string(bytes))
				if err := repo.AppendNote(ci.Ref, commit, note); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// WriteNewComments takes a list of review comments read from GitHub, and writes to the repo any that are new.
//
// The passed in logChan variable is used as our intermediary for logging, and allows us to
// use the same logic for logging messages in either our CLI or our App Engine apps, even though
// the two have different logging frameworks.
func WriteNewComments(r review.Review, repo repository.Repo, logChan chan<- string) error {
	existingComments := comment.ParseAllValid(repo.GetNotes(comment.Ref, r.Revision))
	for _, commentThread := range r.Comments {
		commentNote, err := commentThread.Comment.Write()
		if err != nil {
			return err
		}
		missing := true
		for _, existing := range existingComments {
			if CommentsOverlap(existing, commentThread.Comment) {
				missing = false
			}
		}
		if missing {
			logChan <- fmt.Sprintf("Found a new comment: %q", string(commentNote))
			if err := repo.AppendNote(comment.Ref, r.Revision, commentNote); err != nil {
				return err
			}
		}
	}
	return nil
}

func quoteComment(c comment.Comment) string {
	return fmt.Sprintf("%s:\n\n%s", c.Author, c.Description)
}

func commentDescriptionsMatch(a, b comment.Comment) bool {
	return a.Author == b.Author && a.Description == b.Description
}

func commentDescriptionsOverlap(a, b comment.Comment) bool {
	return commentDescriptionsMatch(a, b) ||
		a.Description == quoteComment(b) ||
		quoteComment(a) == b.Description
}

func commentLocationPathsMatch(a, b comment.Location) bool {
	return a == b ||
		(a.Commit == b.Commit && a.Path == b.Path)
}

func commentLocationsOverlap(a, b comment.Comment) bool {
	return a.Location == b.Location ||
		(a.Location == nil && b.Location.Path == "") ||
		(b.Location == nil && a.Location.Path == "") ||
		(a.Location != nil && b.Location != nil && commentLocationPathsMatch(*a.Location, *b.Location))
}

// CommentsOverlap determines if two review comments are sufficiently similar that one is a good-enough replacement for the other.
//
// The purpose of this method is to account for the semantic differences between the comments on a GitHub
// pull request and the comments on a git-appraise review.
//
// More specifically, GitHub issue comments roughly correspond to git-appraise review-level comments, and
// GitHub pull request comments roughly correspond to git-appraise line-level comments, but with the
// following differences:
//
// 1. None of the GitHub comments can have a parent, but any of the git-appraise ones can.
// 2. The author and timestamp of a GitHub comment is based on the call to the API, so if we want to
//    mirror a comment from git-appraise into GitHub, then when we read that new comment back out this
//    metadata will be different.
// 3. Review-level comments in git-appraise can have a specificed commit, but issue comments can not.
// 4. File-level comments in git-appraise have no corresponding equivalent in GitHub.
// 5. Line-level comments in GitHub have to be anchored in part of the diff, while in git-appraise
//    they can occur anywhere within the file.
//
// To work around these issues, we build in the following leeway:
// 1. We treat two comment descriptions as equivalent if one looks like a quote of the other.
// 2. We treat two locations as equivalent if one of the following holds:
//    0. They actually are the same
//    1. Both are review-level comments, and one of them does not have a commit set
//    2. They are either file-level or line-level comments and occur in the same file
//
// This definition of equivalence does allow some information to be lost when converting from one
// format to the other, but the important information (who said what) gets maintained and we avoid
// accidentally mirroring the same comment back and forth multiple times.
func CommentsOverlap(a, b comment.Comment) bool {
	return a == b ||
		(commentLocationsOverlap(a, b) && commentDescriptionsOverlap(a, b))
}

// WriteNewReviews takes a list of reviews read from GitHub, and writes to the repo any review
// data that has not already been written to it.
//
// The passed in logChan variable is used as our intermediary for logging, and allows us to
// use the same logic for logging messages in either our CLI or our App Engine apps, even though
// the two have different logging frameworks.
func WriteNewReviews(reviews []review.Review, repo repository.Repo, logChan chan<- string) error {
	for _, r := range reviews {
		requestNote, err := r.Request.Write()
		if err != nil {
			return err
		}
		if err != nil {
			return err
		}
		existingRequests := request.ParseAllValid(repo.GetNotes(request.Ref, r.Revision))
		missing := true
		for _, existing := range existingRequests {
			if RequestsOverlap(existing, r.Request) {
				missing = false
			}
		}
		if missing {
			requestJSON, err := r.GetJSON()
			if err != nil {
				return err
			}
			logChan <- fmt.Sprintf("Found a new review for %.12s:\n%s\n", r.Revision, requestJSON)
			if err := repo.AppendNote(request.Ref, r.Revision, requestNote); err != nil {
				return err
			}
		}
		if err := WriteNewComments(r, repo, logChan); err != nil {
			return err
		}
	}
	return nil
}

// RequestsOverlap determines if two review requests are sufficiently similar that one is a good-enough replacement for the other.
//
// The purpose of this method is to account for the semantic differences between a GitHub pull request and a
// git-appraise request. More specifically, a GitHub pull request can only have a single "assignee", but a
// git-appraise review can have multiple reviewers. As such, when we compare two requests to see if they are
// "close enough", we ignore the reviewers field.
func RequestsOverlap(a, b request.Request) bool {
	return a.ReviewRef == b.ReviewRef &&
		a.TargetRef == b.TargetRef &&
		a.Description == b.Description &&
		a.BaseCommit == b.BaseCommit
}
