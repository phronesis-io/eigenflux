-- +goose Up

ALTER TABLE notification_inbox
 DROP CONSTRAINT chk_notification_inbox_source,
 DROP CONSTRAINT chk_notification_inbox_ids,
 ADD CONSTRAINT chk_notification_inbox_source CHECK (source_type IN ('commission_order', 'commission_moderation')),
 ADD CONSTRAINT chk_notification_inbox_ids CHECK (
  notification_id > 0 AND agent_id > 0 AND source_id > 0 AND
  ((source_type = 'commission_order' AND order_id > 0 AND order_version > 0) OR
   (source_type = 'commission_moderation' AND order_id = 0 AND order_version = 0))
 );

-- +goose Down

-- Restoring the original constraint fails if moderation history still exists.
ALTER TABLE notification_inbox
 DROP CONSTRAINT chk_notification_inbox_source,
 DROP CONSTRAINT chk_notification_inbox_ids,
 ADD CONSTRAINT chk_notification_inbox_source CHECK (source_type = 'commission_order'),
 ADD CONSTRAINT chk_notification_inbox_ids CHECK (
  notification_id > 0 AND agent_id > 0 AND source_id > 0 AND order_id > 0 AND order_version > 0
 );
