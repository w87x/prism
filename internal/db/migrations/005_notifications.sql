CREATE TABLE notifications (
  id      bigserial PRIMARY KEY,
  ts      timestamptz NOT NULL DEFAULT now(),
  kind    text NOT NULL,            -- cron | intent | ask | error | info
  level   text NOT NULL DEFAULT 'info',
  title   text NOT NULL,
  text    text NOT NULL DEFAULT '',
  read    boolean NOT NULL DEFAULT false,
  ref     text NOT NULL DEFAULT ''  -- optional page hint: chat | tasks | autonomy
);
CREATE INDEX notifications_ts_idx ON notifications(id DESC);
