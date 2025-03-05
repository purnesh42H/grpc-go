/*
 *
 * Copyright 2025 gRPC authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package testutils

import "regexp"

// FullMatchWithRegex returns whether the full text matches the regex provided.
func FullMatchWithRegex(re *regexp.Regexp, text string) bool {
	if len(text) == 0 {
		return re.MatchString(text)
	}
	re.Longest()
	rem := re.FindString(text)
	return len(rem) == len(text)
}
