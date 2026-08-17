package middleware

import (
	"strings"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// Requests rejected here never reach a channel, so none of the usual billing
// paths run and nothing lands in the log table. That gap is why a customer
// could fail 18 times across two days while our own dashboard showed only
// successes -- the failures existed solely in pod stdout, which rotates within
// the hour. These helpers close it.

// recordDistributorRejection writes a pre-relay rejection to the log table.
//
// channelId is 0 because no channel was ever chosen; that is also what makes
// these rows easy to separate from upstream failures when reading the table.
func recordDistributorRejection(c *gin.Context, modelName, group, message string) {
	userId := c.GetInt("id")
	if userId == 0 {
		// Unauthenticated requests are rejected earlier; without a user there
		// is nothing to attribute the row to and the write would be noise.
		return
	}
	model.RecordErrorLog(
		c,
		userId,
		0,
		modelName,
		c.GetString("token_name"),
		message,
		c.GetInt("token_id"),
		0,
		false,
		group,
		map[string]interface{}{
			"rejected_by":  "distributor",
			"request_path": c.Request.URL.Path,
		},
	)
}

// isKnownModel reports whether any enabled channel anywhere serves this model,
// irrespective of group. A false answer means the name itself is wrong rather
// than temporarily unavailable.
func isKnownModel(modelName string) bool {
	name := strings.TrimSpace(modelName)
	if name == "" {
		return false
	}
	for _, m := range model.GetEnabledModels() {
		if m == name {
			return true
		}
	}
	return false
}

// suggestModel offers the closest known model name, so a typo is answered with
// the fix rather than with a list the caller has to search. Returns "" when
// nothing is close enough to be worth guessing at.
func suggestModel(modelName string) string {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return ""
	}
	best, bestDist := "", -1
	// Roughly a quarter of the name may differ. Tighter than this misses
	// "MinMax-H3" vs "MiniMax-H3"; looser starts proposing unrelated models.
	budget := len(name)/4 + 1
	for _, candidate := range model.GetEnabledModels() {
		d := levenshtein(name, strings.ToLower(candidate))
		if d <= budget && (bestDist < 0 || d < bestDist) {
			best, bestDist = candidate, d
		}
	}
	return best
}

// levenshtein returns the edit distance between a and b, using a single row of
// state since only the distance itself is needed.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
