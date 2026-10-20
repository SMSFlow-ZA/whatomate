package handlers

import (
	"context"
	"time"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
)

func (a *App) resolveExtensionIntake(ctx context.Context, actor addonapi.Actor) (cases.Classification, error) {
	if a.DB == nil || a.Config.Extensions.RosterFreshnessSeconds <= 0 {
		return cases.Classification{}, cases.ErrDenied
	}
	db, e := a.DB.DB()
	if e != nil {
		return cases.Classification{}, e
	}
	store, e := directory.NewPostgresStore(db, actor.OrganizationID, "verified-intake")
	if e != nil {
		return cases.Classification{}, e
	}
	snapshot, e := store.Load(ctx)
	if e != nil {
		return cases.Classification{}, e
	}
	for _, id := range append([]string{actor.UserID}, actor.Aliases...) {
		employee, ok := snapshot.Employees[id]
		if !ok {
			continue
		}
		verified, e := directory.Match(snapshot, employee.Phone, time.Now(), time.Duration(a.Config.Extensions.RosterFreshnessSeconds)*time.Second)
		if e == nil && verified.ID == id && verified.Zone != "" && verified.Department != "" {
			return cases.Classification{Zone: verified.Zone, Department: verified.Department, CaseType: "employee_request"}, nil
		}
	}
	return cases.Classification{}, cases.ErrDenied
}
