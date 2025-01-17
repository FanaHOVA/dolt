// Copyright 2019 Liquidata, Inc.
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

package ref

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBranchName(t *testing.T) {
	assert.Equal(t, true, IsValidBranchName("t"))
	assert.Equal(t, true, IsValidBranchName("☃️"))
	assert.Equal(t, true, IsValidBranchName("user/in-progress/do-some-things"))
	assert.Equal(t, true, IsValidBranchName("user/in-progress/{}"))
	assert.Equal(t, true, IsValidBranchName("user/{/a.tt/}"))

	assert.Equal(t, false, IsValidBranchName(""))
	assert.Equal(t, false, IsValidBranchName("this-is-a-..-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-@{-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a- -test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\t-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-//-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-:-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-?-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-[-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\\-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-^-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-~-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-*-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x00-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x01-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x02-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x03-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x04-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x05-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x06-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x07-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x08-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x09-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0a-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0b-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0c-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0d-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0e-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x0f-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x10-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x11-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x12-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x13-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x14-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x15-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x16-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x17-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x18-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x19-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1a-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1b-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1c-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1d-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1e-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x1f-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\x7f-test"))
	assert.Equal(t, false, IsValidBranchName("this-is-a-\n-test"))
	assert.Equal(t, false, IsValidBranchName("user/working/.tt"))
	assert.Equal(t, false, IsValidBranchName(".user/working/a.tt"))
	assert.Equal(t, false, IsValidBranchName("user/working/"))
	assert.Equal(t, false, IsValidBranchName("/user/working/"))
	assert.Equal(t, false, IsValidBranchName("user/working/mybranch.lock"))
	assert.Equal(t, false, IsValidBranchName("mybranch.lock"))
	assert.Equal(t, false, IsValidBranchName("user.lock/working/mybranch"))
	assert.Equal(t, false, IsValidBranchName("HEAD"))
	assert.Equal(t, false, IsValidBranchName("-"))
}
