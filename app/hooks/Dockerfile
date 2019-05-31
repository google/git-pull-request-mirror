# Copyright 2015 Google Inc. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# First stage that builds the go binary...
FROM gcr.io/gcp-runtimes/go1-builder:1.12 as builder

WORKDIR /go/src/app
COPY . .

RUN apt-get update -yq && \
    apt-get install -yq git-core && \
    export PATH="${PATH}:/usr/local/go/bin" && \
    export GOPATH="/go/" && \
    mkdir -p "${GOPATH}" && \
    go get github.com/google/go-github/github && \
    go get github.com/google/git-appraise/git-appraise && \
    go get github.com/google/git-pull-request-mirror/mirror && \
    go get google.golang.org/appengine && \
    go get golang.org/x/oauth2 && \
    go get cloud.google.com/go/compute/metadata && \
    go get cloud.google.com/go/datastore && \
    cp ${GOPATH}/bin/git-appraise /usr/local/bin/git-appraise

RUN export GOPATH="/go/" && \
    /usr/local/go/bin/go build -o app .

# Second stage that defines the serving app...
FROM gcr.io/distroless/base:latest

COPY --from=builder /usr/bin/* /usr/bin/
COPY --from=builder /usr/local/bin/* /usr/local/bin/
COPY --from=builder /usr/lib/git-core/* /usr/lib/git-core/
COPY --from=builder /usr/share/git-core/* /usr/share/git-core/
COPY --from=builder /lib/x86_64-linux-gnu/* /lib/x86_64-linux-gnu/
COPY --from=builder /usr/lib/x86_64-linux-gnu/* /usr/lib/x86_64-linux-gnu/
COPY --from=builder /go/src/app/app /usr/local/bin/app

CMD ["/usr/local/bin/app"]


