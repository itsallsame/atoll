package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyProfileRegisterCommand atomically creates the non-runnable Profile,
// its provisioning incident, the operator Work, and both audit intents.
func (r *Repository) ApplyProfileRegisterCommand(ctx context.Context, profile model.BrowserProfile,
	incident model.RepairIncident, work model.Work, placement WorkPlacement, receipt model.CommandReceipt,
	profileEvent, repairEvent model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if profile.ProfileID == "" || profile.Version != 2 || profile.AuthStatus != model.ProfileRepairing ||
		profile.SecretRef == "" || profile.Verification == nil || profile.Verification.Validate(profile.SecurityDomain) != nil ||
		incident.Domain != model.FailureProfile || incident.DomainKey != profile.ProfileID ||
		incident.FailureSignature != "profile_unprovisioned" || incident.FailingVersion != "profile:1" ||
		incident.Status != model.RepairOpen || incident.Version != 1 || incident.RepairWorkID != work.WorkID ||
		len(incident.AffectedWorkIDs) != 0 || work.TargetType != "repair_incident" || work.TargetID != incident.IncidentID ||
		work.Purpose != "repair" || work.Trigger != "human" || work.Status != model.WorkOpen || work.Version != 1 ||
		work.InitiatorActorID == "" || work.CauseMessageID == "" || work.CauseWorkID != "" ||
		placement.BusinessKey != "repair|profile-provision|"+profile.ProfileID || placement.Capability != "" ||
		placement.Origin != "" || placement.ProfileID != "" || !placement.NotBefore.Equal(businessAt) ||
		receipt.CommandID == "" || profileEvent.Kind != "profile.registered" ||
		profileEvent.AggregateType != "profile" || profileEvent.AggregateID != profile.ProfileID ||
		profileEvent.AggregateVersion != profile.Version || profileEvent.CauseCommandID != receipt.CommandID ||
		repairEvent.Kind != "repair.opened" || repairEvent.AggregateType != "repair_incident" ||
		repairEvent.AggregateID != incident.IncidentID || repairEvent.AggregateVersion != incident.Version ||
		repairEvent.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("Profile register facts are inconsistent")
	}
	profileAt, profileTimeErr := time.Parse(time.RFC3339, profileEvent.BusinessAt)
	repairAt, repairTimeErr := time.Parse(time.RFC3339, repairEvent.BusinessAt)
	if profileTimeErr != nil || repairTimeErr != nil || !profileAt.Equal(businessAt) || !repairAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Profile register business time is inconsistent")
	}
	for _, publicPayload := range [][]byte{receipt.Response, profileEvent.Payload, repairEvent.Payload} {
		if jsonPayloadContainsString(publicPayload, profile.SecretRef) || jsonPayloadContainsString(publicPayload, profile.DeviceID) {
			return CommandResult{}, fmt.Errorf("Profile register public facts contain private Profile references")
		}
	}
	profileState, _ := json.Marshal(profile)
	incidentState, _ := json.Marshal(incident)
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_profiles(
  profile_id, security_domain, device_id, secret_ref, auth_status, version, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, profile.ProfileID, profile.SecurityDomain, profile.DeviceID,
		profile.SecretRef, profile.AuthStatus, profile.Version, profileState, businessAt.UTC())
	if err != nil {
		return CommandResult{}, mapProfileRegisterDuplicate(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_repair_incidents(
  incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature,
  failing_version, repair_work_id, repair_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, incident.IncidentID, incident.RepairKey, incident.RepairKey,
		incident.Domain, incident.DomainKey, incident.FailureSignature, incident.FailingVersion, incident.RepairWorkID,
		incident.Status, incident.Version, incidentState, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		return CommandResult{}, mapProfileRegisterDuplicate(err)
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, profileEvent, profileAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, repairEvent, repairAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func mapProfileRegisterDuplicate(err error) error {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: Profile or active provisioning repair", ErrBusinessKeyExists)
	}
	return err
}
