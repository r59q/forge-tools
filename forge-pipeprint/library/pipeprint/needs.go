package pipeprint

// Levels returns each job's level: 1 for jobs without needs, and otherwise
// one more than the highest level among its needs. Unknown needs are
// ignored. Jobs on or after a cycle get level 0; Validate rejects cycles.
func (p *Pipeline) Levels() map[string]int {
	byID := jobsByID(p.Jobs)
	levels := map[string]int{}
	visiting := map[string]bool{}

	var level func(id string) int
	level = func(id string) int {
		if l, ok := levels[id]; ok {
			return l
		}

		if visiting[id] {
			return -1
		}

		visiting[id] = true
		highest := 0
		for _, need := range byID[id].Needs {
			if _, ok := byID[need]; !ok {
				continue
			}

			l := level(need)
			if l < 0 {
				return -1
			}

			highest = max(highest, l)
		}

		visiting[id] = false
		levels[id] = highest + 1

		return highest + 1
	}

	for _, job := range p.Jobs {
		if level(job.ID) < 0 {
			levels[job.ID] = 0
		}
	}

	return levels
}

// Job returns the job with id, or nil.
func (p *Pipeline) Job(id string) *Job {
	for i := range p.Jobs {
		if p.Jobs[i].ID == id {
			return &p.Jobs[i]
		}
	}

	return nil
}

// findCycle returns the ID of a job on a needs cycle, or "".
func findCycle(jobs []Job) string {
	levels := (&Pipeline{Jobs: jobs}).Levels()
	for _, job := range jobs {
		if levels[job.ID] == 0 {
			return job.ID
		}
	}

	return ""
}

func jobsByID(jobs []Job) map[string]Job {
	byID := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		byID[job.ID] = job
	}

	return byID
}
