package event

import (
	"context"
	"reflect"

	"github.com/jinzhu/gorm"
	errs "github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"

	"github.com/fabric8-services/fabric8-wit/account"
	"github.com/fabric8-services/fabric8-wit/errors"
	"github.com/fabric8-services/fabric8-wit/workitem"
)

// APIStringTypeEvents represent the type of event
const APIStringTypeEvents = "events"

// Repository encapsulates retrieval of work item events
type Repository interface {
	//repository.Exister
	List(ctx context.Context, wiID uuid.UUID) ([]Event, error)
}

// NewEventRepository creates a work item event repository based on gorm
func NewEventRepository(db *gorm.DB) *GormEventRepository {
	return &GormEventRepository{
		db:               db,
		workItemRepo:     workitem.NewWorkItemRepository(db),
		wiRevisionRepo:   workitem.NewRevisionRepository(db),
		workItemTypeRepo: workitem.NewWorkItemTypeRepository(db),
		identityRepo:     account.NewIdentityRepository(db),
	}
}

// GormEventRepository represents the Gorm model
type GormEventRepository struct {
	db               *gorm.DB
	workItemRepo     *workitem.GormWorkItemRepository
	wiRevisionRepo   *workitem.GormRevisionRepository
	workItemTypeRepo *workitem.GormWorkItemTypeRepository
	identityRepo     *account.GormIdentityRepository
}

// List return the events
func (r *GormEventRepository) List(ctx context.Context, wiID uuid.UUID) ([]Event, error) {
	revisionList, err := r.wiRevisionRepo.List(ctx, wiID)
	if err != nil {
		return nil, errs.Wrapf(err, "failed to list revisions for work item: %s", wiID)
	}
	if revisionList == nil {
		return []Event{}, nil
	}
	if err = r.workItemRepo.CheckExists(ctx, wiID); err != nil {
		return nil, errs.Wrapf(err, "failed to find work item: %s", wiID)
	}

	eventList := []Event{}
	for k := 1; k < len(revisionList); k++ {

		oldRev := revisionList[k-1]
		newRev := revisionList[k]

		// If the new and old work item type are different, we're skipping this
		// revision because it denotes the change of a work item type.
		//
		// TODO(kwk): make sure we have a proper "changed work item type"
		// revision entry in one way or another.
		if oldRev.WorkItemTypeID != newRev.WorkItemTypeID {
			continue
		}

		wit, err := r.workItemTypeRepo.Load(ctx, oldRev.WorkItemTypeID)
		if err != nil {
			return nil, errs.Wrapf(err, "failed to load old work item type: %s", oldRev.WorkItemTypeID)
		}

		modifierID, err := r.identityRepo.Load(ctx, newRev.ModifierIdentity)
		if err != nil {
			return nil, errs.Wrapf(err, "failed to load modifier identity %s", newRev.ModifierIdentity)
		}

		for fieldName, fieldDef := range wit.Fields {

			oldVal := oldRev.WorkItemFields[fieldName]
			newVal := newRev.WorkItemFields[fieldName]

			event := Event{
				ID:             newRev.ID,
				Name:           fieldName,
				WorkItemTypeID: newRev.WorkItemTypeID,
				Timestamp:      newRev.Time,
				Modifier:       modifierID.ID,
				Old:            oldVal,
				New:            newVal,
			}

			// The enum type can be handled by the simple type since it's just a
			// single value after all.
			ft := fieldDef.Type
			enumType, isEnumType := ft.(workitem.EnumType)
			if isEnumType {
				ft = enumType.BaseType
			}

			switch fieldType := ft.(type) {
			case workitem.ListType:
				var p, n []interface{}
				var ok bool

				switch t := oldVal.(type) {
				case nil:
					p = []interface{}{}
				case []interface{}:
					converted, err := fieldType.ConvertFromModel(t)
					if err != nil {
						return nil, errs.Wrapf(err, "failed to convert old value for field %s from storage representation: %+v", fieldName, t)
					}
					p, ok = converted.([]interface{})
					if !ok {
						return nil, errs.Errorf("failed to convert old value for field %s from to []interface{}: %+v", fieldName, t)
					}
				}

				switch t := newVal.(type) {
				case nil:
					n = []interface{}{}
				case []interface{}:
					converted, err := fieldType.ConvertFromModel(t)
					if err != nil {
						return nil, errs.Wrapf(err, "failed to convert new value for field %s from storage representation: %+v", fieldName, t)
					}
					n, ok = converted.([]interface{})
					if !ok {
						return nil, errs.Errorf("failed to convert new value for field %s from to []interface{}: %+v", fieldName, t)
					}
				}

				// Avoid duplicate entries for empty labels or assignees, etc.
				if !reflect.DeepEqual(p, n) {
					event.Old = p
					event.New = n
					eventList = append(eventList, event)
				}
			case workitem.SimpleType:
				// compensate conversion from storage if this really was an enum field
				converter := fieldType.ConvertFromModel
				if isEnumType {
					converter = enumType.ConvertFromModel
				}

				p, err := converter(oldVal)
				if err != nil {
					return nil, errs.Wrapf(err, "failed to convert old value for field %s from storage representation: %+v", fieldName, oldVal)
				}
				n, err := converter(newVal)
				if err != nil {
					return nil, errs.Wrapf(err, "failed to convert new value for field %s from storage representation: %+v", fieldName, newVal)
				}
				if !reflect.DeepEqual(p, n) {
					event.Old = p
					event.New = n
					eventList = append(eventList, event)
				}
			default:
				return nil, errors.NewNotFoundError("unknown field type", fieldType.GetKind().String())
			}
		}
	}
	return eventList, nil
}
