package commissionapi

type CommissionInput struct {
	Title                 string   `json:"title"`
	CapabilityDescription string   `json:"capability_description"`
	RequestSpecText       string   `json:"request_spec_text"`
	DeliverySpecText      string   `json:"delivery_spec_text"`
	Tags                  []string `json:"tags"`
	PriceFen              int64    `json:"price_fen"`
	Currency              string   `json:"currency"`
	PromisedDeliveryMS    int64    `json:"promised_delivery_ms"`
	RequestSpecSchema     string   `json:"request_spec_schema"`
	DeliverySpecSchema    string   `json:"delivery_spec_schema"`
}

type TransferGrant struct {
	ObjectID  int64             `json:"object_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt int64             `json:"expires_at"`
}

type TransferGrantData struct {
	Grant TransferGrant `json:"grant"`
}
