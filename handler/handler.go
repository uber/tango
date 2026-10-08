// Copyright (c) 2026 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"github.com/uber/tango/controller"
	pb "github.com/uber/tango/tangopb"
)

type Params struct {
	Controller controller.Controller
}

type handler struct {
	controller controller.Controller
}

func New(p Params) pb.TangoYARPCServer {
	return &handler{controller: p.Controller}
}
