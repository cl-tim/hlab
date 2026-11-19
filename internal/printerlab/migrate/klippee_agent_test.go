package migrate

import "testing"

func TestParseKlippeeAgentRefreshRequiresFixedSafeService(t *testing.T) {
	raw := []byte(`{
		"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
		"service":"klippee-moonraker.service","scope":"user","account":"mks",
		"observed_state":"active","restart_exit":0,"state_exit":0,
		"klipper_actions_performed":false,"config_deployment_performed":false
	}`)
	result, err := ParseKlippeeAgentRefresh(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("expected success")
	}

	unsafe := []byte(`{
		"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
		"service":"klipper.service","scope":"system","account":"root",
		"observed_state":"active","restart_exit":0,"state_exit":0,
		"klipper_actions_performed":true,"config_deployment_performed":false
	}`)
	if _, err := ParseKlippeeAgentRefresh(unsafe); err == nil {
		t.Fatal("expected unsafe service rejection")
	}

	for name, missingProof := range map[string][]byte{
		"restart exit": []byte(`{
			"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
			"service":"klippee-moonraker.service","scope":"user","account":"mks",
			"observed_state":"active","state_exit":0,
			"klipper_actions_performed":false,"config_deployment_performed":false
		}`),
		"state exit": []byte(`{
			"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
			"service":"klippee-moonraker.service","scope":"user","account":"mks",
			"observed_state":"active","restart_exit":0,
			"klipper_actions_performed":false,"config_deployment_performed":false
		}`),
	} {
		t.Run("missing "+name, func(t *testing.T) {
			if _, err := ParseKlippeeAgentRefresh(missingProof); err == nil {
				t.Fatal("expected missing proof rejection")
			}
		})
	}
}
