-- Migration factory, wave 49 (#49): the document count of a parallel-run variance. The rounding
-- rule RND-001 allows 0.01 per document (BR-49.7), so the tolerance of a trial-balance tuple scales
-- with the number of legacy documents it sums; the count is kept with the variance for review.
-- 0 for a variance stored before tuples carried a count, or for a tuple that gave none: the rule
-- then allows one document's worth.
SET LOCAL lock_timeout = '5s';

alter table variance add column documents int not null default 0 check (documents >= 0);
