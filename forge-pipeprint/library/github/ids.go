package github

import (
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// jobIDs maps each job ID to a valid, unique workflow job ID: letters,
// digits, - and _, starting with a letter or _.
func jobIDs(jobs []pipeprint.Job) map[string]string {
	ids := make(map[string]string, len(jobs))
	taken := map[string]bool{}
	for _, job := range jobs {
		if validID(job.ID) {
			ids[job.ID] = job.ID
			taken[job.ID] = true
		}
	}

	for _, job := range jobs {
		if _, ok := ids[job.ID]; ok {
			continue
		}

		id := sanitize(job.ID)
		for n := 2; taken[id]; n++ {
			id = sanitize(job.ID) + "-" + strconv.Itoa(n)
		}

		ids[job.ID] = id
		taken[id] = true
	}

	return ids
}

func validID(id string) bool {
	return id != "" && sanitize(id) == id
}

// sanitize replaces characters not allowed in job IDs with -.
func sanitize(id string) string {
	var b strings.Builder
	for i, r := range id {
		allowed := isLetter(r) || r == '_' || (i > 0 && (isDigit(r) || r == '-'))
		switch {
		case allowed:
			b.WriteRune(r)
		case i == 0 && (isDigit(r) || r == '-'):
			b.WriteRune('_')
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	return b.String()
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}
