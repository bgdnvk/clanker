package maker

import (
	"encoding/json"
	"testing"
)

func TestInferSGBindings(t *testing.T) {
	tests := []struct {
		name      string
		groupName string
		groupID   string
		wantKeys  []string
	}{
		{
			name:      "empty group name",
			groupName: "",
			groupID:   "sg-123",
			wantKeys:  nil,
		},
		{
			name:      "empty group ID",
			groupName: "alb-sg",
			groupID:   "",
			wantKeys:  nil,
		},
		{
			name:      "simple rds-sg",
			groupName: "rds-sg",
			groupID:   "sg-rds111",
			wantKeys:  []string{"SG_RDS", "SG_RDS_ID", "RDS_SG_ID", "RDS_SG"},
		},
		{
			name:      "lambda-sg",
			groupName: "lambda-sg",
			groupID:   "sg-lam222",
			wantKeys:  []string{"SG_LAMBDA", "SG_LAMBDA_ID", "LAMBDA_SG_ID"},
		},
		{
			name:      "multi-keyword name",
			groupName: "myapp-db-sg",
			groupID:   "sg-multi",
			wantKeys:  []string{"SG_MYAPP", "SG_MYAPP_ID", "SG_DB", "SG_DB_ID", "SG_MYAPP_DB", "SG_MYAPP_DB_ID"},
		},
		{
			name:      "name with security-group suffix",
			groupName: "web-security-group",
			groupID:   "sg-web333",
			wantKeys:  []string{"SG_WEB", "SG_WEB_ID", "WEB_SG_ID"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bindings := make(map[string]string)
			inferSGBindings(tt.groupName, tt.groupID, bindings)

			if tt.wantKeys == nil {
				if len(bindings) != 0 {
					t.Errorf("expected no bindings, got %v", bindings)
				}
				return
			}

			for _, key := range tt.wantKeys {
				val, ok := bindings[key]
				if !ok {
					t.Errorf("missing expected binding key %q", key)
					continue
				}
				if val != tt.groupID {
					t.Errorf("binding[%q] = %q, want %q", key, val, tt.groupID)
				}
			}
		})
	}
}

func sgCreateOutput(groupID string) string {
	data := map[string]any{"GroupId": groupID}
	b, _ := json.Marshal(data)
	return string(b)
}

func TestLearnPlanBindings_SGFillOrder(t *testing.T) {
	t.Run("single SG fills SG_ID first", func(t *testing.T) {
		bindings := make(map[string]string)
		args := []string{"ec2", "create-security-group", "--group-name", "app-sg"}
		output := sgCreateOutput("sg-aaa")
		learnPlanBindings(args, output, bindings, 0)

		if bindings["SG_ID"] != "sg-aaa" {
			t.Errorf("SG_ID = %q, want %q", bindings["SG_ID"], "sg-aaa")
		}
	})

	t.Run("multiple SGs fill slots in order", func(t *testing.T) {
		bindings := make(map[string]string)

		// First SG: alb-sg
		args1 := []string{"ec2", "create-security-group", "--group-name", "alb-sg"}
		learnPlanBindings(args1, sgCreateOutput("sg-alb"), bindings, 0)

		// Second SG: web-sg
		args2 := []string{"ec2", "create-security-group", "--group-name", "web-sg"}
		learnPlanBindings(args2, sgCreateOutput("sg-web"), bindings, 1)

		// Third SG: rds-sg
		args3 := []string{"ec2", "create-security-group", "--group-name", "rds-sg"}
		learnPlanBindings(args3, sgCreateOutput("sg-rds"), bindings, 2)

		// Named bindings should be set correctly via inferSGBindings and switch block
		if bindings["SG_ALB_ID"] != "sg-alb" {
			t.Errorf("SG_ALB_ID = %q, want %q", bindings["SG_ALB_ID"], "sg-alb")
		}
		if bindings["SG_WEB_ID"] != "sg-web" {
			t.Errorf("SG_WEB_ID = %q, want %q", bindings["SG_WEB_ID"], "sg-web")
		}
		if bindings["SG_RDS_ID"] != "sg-rds" {
			t.Errorf("SG_RDS_ID = %q, want %q", bindings["SG_RDS_ID"], "sg-rds")
		}

		// All named bindings should be distinct
		if bindings["SG_ALB_ID"] == bindings["SG_WEB_ID"] {
			t.Error("SG_ALB_ID and SG_WEB_ID should not be equal")
		}
		if bindings["SG_WEB_ID"] == bindings["SG_RDS_ID"] {
			t.Error("SG_WEB_ID and SG_RDS_ID should not be equal")
		}
		if bindings["SG_ALB_ID"] == bindings["SG_RDS_ID"] {
			t.Error("SG_ALB_ID and SG_RDS_ID should not be equal")
		}
	})

	t.Run("fill-order does not clobber named SG bindings", func(t *testing.T) {
		// This test verifies the fix: the generic fill-order loop only fills
		// SG_ID and SG_1, not SG_ALB_ID/SG_WEB_ID/SG_RDS_ID. Previously,
		// the first SG would fill SG_ID, SG_1, then SG_ALB_ID (clobbering
		// the value that inferSGBindings/switch was supposed to set).
		bindings := make(map[string]string)

		// First: alb-sg
		args1 := []string{"ec2", "create-security-group", "--group-name", "alb-sg"}
		learnPlanBindings(args1, sgCreateOutput("sg-alb"), bindings, 0)

		// Second: web-sg
		args2 := []string{"ec2", "create-security-group", "--group-name", "web-sg"}
		learnPlanBindings(args2, sgCreateOutput("sg-web"), bindings, 1)

		// Third: rds-sg
		args3 := []string{"ec2", "create-security-group", "--group-name", "rds-sg"}
		learnPlanBindings(args3, sgCreateOutput("sg-rds"), bindings, 2)

		// Named bindings must each hold the correct SG ID
		if bindings["SG_ALB_ID"] != "sg-alb" {
			t.Errorf("SG_ALB_ID = %q, want sg-alb (fill-order clobbered it)", bindings["SG_ALB_ID"])
		}
		if bindings["SG_WEB_ID"] != "sg-web" {
			t.Errorf("SG_WEB_ID = %q, want sg-web (fill-order clobbered it)", bindings["SG_WEB_ID"])
		}
		if bindings["SG_RDS_ID"] != "sg-rds" {
			t.Errorf("SG_RDS_ID = %q, want sg-rds (fill-order clobbered it)", bindings["SG_RDS_ID"])
		}
	})

	t.Run("generic SG does not clobber named bindings", func(t *testing.T) {
		bindings := make(map[string]string)

		// First: generic SG
		args1 := []string{"ec2", "create-security-group", "--group-name", "default-sg"}
		learnPlanBindings(args1, sgCreateOutput("sg-def"), bindings, 0)

		// Second: alb-sg (named)
		args2 := []string{"ec2", "create-security-group", "--group-name", "alb-sg"}
		learnPlanBindings(args2, sgCreateOutput("sg-alb"), bindings, 1)

		// SG_ID should be the first SG
		if bindings["SG_ID"] != "sg-def" {
			t.Errorf("SG_ID = %q, want %q", bindings["SG_ID"], "sg-def")
		}
		// SG_ALB_ID should be the alb SG
		if bindings["SG_ALB_ID"] != "sg-alb" {
			t.Errorf("SG_ALB_ID = %q, want %q", bindings["SG_ALB_ID"], "sg-alb")
		}
	})
}
