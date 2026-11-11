-- The zone a worker's work days are reckoned in, normally the time zone of the site the worker works at (#543). The job-card
-- rate on registration.posted is the dated rate effective on the day the work started, and that day is the local day in this
-- zone, not the UTC day: work started at 22:30 in Chicago on 20 October belongs to 20 October. UTC is the default, which is
-- what every earlier registration was priced with. Adding a column with a constant default takes no table rewrite.
ALTER TABLE worker ADD COLUMN IF NOT EXISTS time_zone text NOT NULL DEFAULT 'UTC' CHECK (char_length(time_zone) BETWEEN 1 AND 64);
