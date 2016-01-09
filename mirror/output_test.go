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
	"testing"

	"github.com/google/git-appraise/review/comment"
	"github.com/google/git-appraise/review/request"
)

func TestCommentsOverlap(t *testing.T) {
	reviewLevelComment := comment.Comment{
		Timestamp: "00000000",
		Author:    "user@example.com",
		Location: &comment.Location{
			Commit: "ABCDEFG",
		},
		Description: "Please fix so and so...",
	}
	if !CommentsOverlap(reviewLevelComment, reviewLevelComment) {
		t.Fatal("Erroneous distinction drawn between identical review-level comments")
	}

	repeatedReviewLevelComment := comment.Comment{
		Timestamp: "00000000",
		Author:    "user@example.com",
		Location: &comment.Location{
			Commit: "ABCDEFH",
		},
		Description: "Please fix so and so...",
	}
	if CommentsOverlap(reviewLevelComment, repeatedReviewLevelComment) {
		t.Fatal("Failed to distinguish between review comments at different commits")
	}

	issueComment := comment.Comment{
		Timestamp:   "FFFFFFFF",
		Author:      "user@example.com",
		Description: "Please fix so and so...",
	}
	if !CommentsOverlap(reviewLevelComment, issueComment) {
		t.Fatal("Erroneous distinction drawn between a review-level comment and an issue comment")
	}
	reviewLevelCommentHash, err := reviewLevelComment.Hash()
	if err != nil {
		t.Fatal(err)
	}
	reviewLevelChildComment := comment.Comment{
		Timestamp: "FFFFFFFG",
		Author:    "user2@example.com",
		Parent:    reviewLevelCommentHash,
		Location: &comment.Location{
			Commit: "ABCDEFG",
		},
		Description: "Done",
	}
	issueChildComment := comment.Comment{
		Timestamp:   "FFFFFFFH",
		Author:      "user2@example.com",
		Description: "Done",
	}
	if !CommentsOverlap(reviewLevelChildComment, issueChildComment) {
		t.Fatal("Erroneous distinction drawn between a review-level child comment and an issue comment")
	}
}

func TestRequestsOverlap(t *testing.T) {
	request1 := request.Request{
		Timestamp:   "00000000",
		Requester:   "user@example.com",
		TargetRef:   "refs/heads/dev",
		ReviewRef:   "refs/pull/42/head",
		Description: "Bug fixes",
		BaseCommit:  "ABCDEFG",
	}
	if !RequestsOverlap(request1, request1) {
		t.Fatal("Identical requests were not determined to overlap")
	}

	request2 := request.Request{
		Timestamp:   "FFFFFFFF",
		Requester:   request1.Requester,
		TargetRef:   request1.TargetRef,
		ReviewRef:   request1.ReviewRef,
		Description: request1.Description,
		BaseCommit:  request1.BaseCommit,
	}
	if !RequestsOverlap(request1, request2) {
		t.Fatal("Timestamps should not be used for determining overlap")
	}

	request3 := request.Request{
		Timestamp:   request1.Timestamp,
		Requester:   request1.Requester,
		TargetRef:   "refs/heads/master",
		ReviewRef:   request1.ReviewRef,
		Description: request1.Description,
		BaseCommit:  request1.BaseCommit,
	}
	if RequestsOverlap(request1, request3) {
		t.Fatal("Requests with different targets should not overlap")
	}
}
