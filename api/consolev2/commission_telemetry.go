package consolev2

import "errors"

// Console action observations are distinct from authoritative payment or order
// transitions. Opening payment successfully does not establish payment received.
func validateCommissionOrderAction(properties map[string]interface{}) error {
	for key := range properties {
		if !maintenanceOneOf(key, "route", "action", "result", "entity_type", "entity_id") {
			return errors.New("commission action property is not allowed")
		}
	}
	action, _ := properties["action"].(string)
	result, _ := properties["result"].(string)
	if !maintenanceOneOf(action, "open_payment", "copy_order_instruction") || !maintenanceOneOf(result, "started", "succeeded", "failed") || properties["route"] != "/dashboard/trade" || properties["entity_type"] != "commission_order" {
		return errors.New("invalid commission action category")
	}
	if rawID, present := properties["entity_id"]; present {
		id, ok := rawID.(string)
		if !ok || !canonicalPositiveNotificationID(id) {
			return errors.New("invalid commission action order ID")
		}
	}
	return nil
}
