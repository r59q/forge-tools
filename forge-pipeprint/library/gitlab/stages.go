package gitlab

import (
	"slices"
	"strconv"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// GitLab's stage defaults.
const (
	stagePre     = ".pre"
	stagePost    = ".post"
	defaultStage = "test"
)

// defaultStages are the stages GitLab uses without a stages keyword.
func defaultStages() []string {
	return []string{"build", defaultStage, "deploy"}
}

// stageOrder returns every stage in run order, with .pre first and .post
// last.
func stageOrder(stages []string) []string {
	if stages == nil {
		stages = defaultStages()
	}

	order := []string{stagePre}
	for _, s := range stages {
		if s != stagePre && s != stagePost {
			order = append(order, s)
		}
	}

	return append(order, stagePost)
}

// implicitNeeds returns what each job waits for without needs: every job
// in the nearest earlier stage that has jobs.
func implicitNeeds(jobs []pipeprint.Job, stageOf map[string]string, order []string) map[string][]string {
	byStage := map[string][]string{}
	for _, job := range jobs {
		byStage[stageOf[job.ID]] = append(byStage[stageOf[job.ID]], job.ID)
	}

	needs := map[string][]string{}
	for _, job := range jobs {
		at := slices.Index(order, stageOf[job.ID])
		for i := at - 1; i >= 0; i-- {
			if earlier := byStage[order[i]]; len(earlier) > 0 {
				needs[job.ID] = earlier
				break
			}
		}
	}

	return needs
}

// plan is where each job goes when writing stages.
type plan struct {
	// stages is the stages keyword, or nil to leave it out.
	stages []string
	// order is every stage in run order.
	order []string
	// stageOf maps job IDs to stages.
	stageOf map[string]string
	// write says whether to write each job's stage.
	write map[string]bool
}

// planStages keeps the pipeline's stages when it has any. Otherwise it
// derives one stage per needs level, or none when every job is on level 1.
func planStages(p *pipeprint.Pipeline, w *pipeprint.Warnings) plan {
	if p.Stages == nil && !anyStage(p.Jobs) {
		return levelStages(p)
	}

	pl := plan{stages: p.Stages, stageOf: map[string]string{}, write: map[string]bool{}}
	order := stageOrder(p.Stages)
	for _, job := range p.Jobs {
		stage := job.Stage
		if stage == "" {
			stage = defaultStage
		}

		if !slices.Contains(order, stage) {
			w.Add("jobs."+job.ID+".stage", "stage "+strconv.Quote(stage)+" added to stages")
			pl.stages = append(slices.Clone(stageList(pl.stages)), stage)
			order = stageOrder(pl.stages)
		}

		pl.stageOf[job.ID] = stage
		pl.write[job.ID] = job.Stage != ""
	}

	pl.order = order

	return pl
}

// stageList returns stages, or GitLab's defaults when nil.
func stageList(stages []string) []string {
	if stages == nil {
		return defaultStages()
	}

	return stages
}

func levelStages(p *pipeprint.Pipeline) plan {
	levels := p.Levels()
	highest := 0
	for _, l := range levels {
		highest = max(highest, l)
	}

	pl := plan{stageOf: map[string]string{}, write: map[string]bool{}}
	if highest <= 1 {
		for _, job := range p.Jobs {
			pl.stageOf[job.ID] = defaultStage
		}

		pl.order = stageOrder(nil)

		return pl
	}

	for l := 1; l <= highest; l++ {
		pl.stages = append(pl.stages, "stage-"+strconv.Itoa(l))
	}

	for _, job := range p.Jobs {
		pl.stageOf[job.ID] = "stage-" + strconv.Itoa(levels[job.ID])
		pl.write[job.ID] = true
	}

	pl.order = stageOrder(pl.stages)

	return pl
}

func anyStage(jobs []pipeprint.Job) bool {
	for _, job := range jobs {
		if job.Stage != "" {
			return true
		}
	}

	return false
}

// sameSet reports whether a and b hold the same IDs.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
