// Copyright © 2018 Heptio
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

package envoy

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heptio/contour/internal/dag"
)

// Clustername returns the name of the CDS cluster for this service.
func Clustername(service *dag.Service) string {
	buf := service.LoadBalancerStrategy
	if hc := service.HealthCheck; hc != nil {
		if hc.TimeoutSeconds > 0 {
			buf += (time.Duration(hc.TimeoutSeconds) * time.Second).String()
		}
		if hc.IntervalSeconds > 0 {
			buf += (time.Duration(hc.IntervalSeconds) * time.Second).String()
		}
		if hc.UnhealthyThresholdCount > 0 {
			buf += strconv.Itoa(int(hc.UnhealthyThresholdCount))
		}
		if hc.HealthyThresholdCount > 0 {
			buf += strconv.Itoa(int(hc.HealthyThresholdCount))
		}
		buf += hc.Path
	}

	hash := sha1.Sum([]byte(buf))
	ns := service.Namespace()
	name := service.Name()
	return Hashname(60, ns, name, strconv.Itoa(int(service.Port)), fmt.Sprintf("%x", hash[:5]))
}

// Hashname takes a lenth l and a varargs of strings s and returns a string whose length
// which does not exceed l. Internally s is joined with strings.Join(s, "/"). If the
// combined length exceeds l then hashname truncates each element in s, starting from the
// end using a hash derived from the contents of s (not the current element). This process
// continues until the length of s does not exceed l, or all elements have been truncated.
// In which case, the entire string is replaced with a hash not exceeding the length of l.
func Hashname(l int, s ...string) string {
	const shorthash = 6 // the length of the shorthash

	r := strings.Join(s, "/")
	if l > len(r) {
		// we're under the limit, nothing to do
		return r
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(r)))
	for n := len(s) - 1; n >= 0; n-- {
		s[n] = truncate(l/len(s), s[n], hash[:shorthash])
		r = strings.Join(s, "/")
		if l > len(r) {
			return r
		}
	}
	// truncated everything, but we're still too long
	// just return the hash truncated to l.
	return hash[:min(len(hash), l)]
}

// truncate truncates s to l length by replacing the
// end of s with -suffix.
func truncate(l int, s, suffix string) string {
	if l >= len(s) {
		// under the limit, nothing to do
		return s
	}
	if l <= len(suffix) {
		// easy case, just return the start of the suffix
		return suffix[:min(l, len(suffix))]
	}
	return s[:l-len(suffix)-1] + "-" + suffix
}

func min(a, b int) int {
	if a > b {
		return b
	}
	return a
}