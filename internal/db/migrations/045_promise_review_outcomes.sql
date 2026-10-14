-- A promise review that was acted on can now end: 'done' (the follow-up kept the promise) or 'superseded' (the follow-up broke
-- it again and has its own review).
ALTER TABLE promise_reviews DROP CONSTRAINT IF EXISTS promise_reviews_status_check;
ALTER TABLE promise_reviews ADD CONSTRAINT promise_reviews_status_check CHECK (status IN ('open','fixing','dismissed','done','superseded'));
