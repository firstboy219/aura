-- Messaging channel (Blueprint D.1 P5b, OL-11, ADR-0044): each owner notification is delivered once per channel.
CREATE TABLE channel_deliveries (
  channel TEXT NOT NULL, item TEXT NOT NULL, delivered_at INTEGER NOT NULL,
  PRIMARY KEY (channel, item));
