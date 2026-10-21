package agent

import "time"

// llmCallTimeout bounds one model call made on the side of the work (a judgement, a profile, a review). It is generous on purpose: a slow
// local model needs minutes for an answer with a big prompt, and a short deadline made these calls fail quietly — an acceptance check that
// "cannot run" lets every report through, a restart review that times out decides nothing.
const llmCallTimeout = 15 * time.Minute
