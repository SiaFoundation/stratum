package server

type (
	StratumRequest struct {
		ID     any    `json:"id"` // could be a string or integer because we can't have nice things
		Method string `json:"method"`
		Params []any  `json:"params"`
	}

	StratumResponse struct {
		ID     any     `json:"id"` // could be a string or integer because we can't have nice things
		Result any     `json:"result"`
		Error  *string `json:"error"`
	}
)
