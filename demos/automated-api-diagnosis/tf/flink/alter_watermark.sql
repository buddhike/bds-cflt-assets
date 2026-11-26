ALTER TABLE `gateway-logs`
MODIFY WATERMARK FOR `timestamp` AS `timestamp` - INTERVAL '5' SECOND
