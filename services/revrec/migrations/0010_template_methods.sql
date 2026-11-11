-- #29: a recognition template may also name MILESTONE (the obligation waits for its satisfaction, POST /milestones/{id}:satisfy)
-- or USAGE (no schedule until usage posts; the contract stays open). Neither takes a term.
ALTER TABLE recognition_template DROP CONSTRAINT IF EXISTS recognition_template_method_check;
ALTER TABLE recognition_template ADD CONSTRAINT recognition_template_method_check
  CHECK (method IN ('POINT_IN_TIME', 'RATABLE', 'MILESTONE', 'USAGE'));
