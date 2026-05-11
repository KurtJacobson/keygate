package handler

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82/subscription"

	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/apperr"
	"github.com/tabloy/keygate/pkg/response"
)

type AdminHandler struct {
	Store   *store.Store
	Webhook *service.WebhookService
	Email   *service.EmailService
	Expiry  *service.ExpiryChecker
	Metered *service.MeteredBillingSyncer
}

func NewAdminHandler(s *store.Store, wh *service.WebhookService, em *service.EmailService, ex *service.ExpiryChecker, ms *service.MeteredBillingSyncer) *AdminHandler {
	return &AdminHandler{Store: s, Webhook: wh, Email: em, Expiry: ex, Metered: ms}
}

// ─── Stats ───

func (h *AdminHandler) Stats(c *gin.Context) {
	stats, err := h.Store.GetStats(c)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, stats)
}

// ─── Products ───

func (h *AdminHandler) ListProducts(c *gin.Context) {
	products, err := h.Store.ListProducts(c, c.Query("search"))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"products": products})
}

func (h *AdminHandler) GetProduct(c *gin.Context) {
	p, err := h.Store.FindProductByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "product not found")
		return
	}
	response.OK(c, p)
}

func (h *AdminHandler) CreateProduct(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
		Slug string `json:"slug" binding:"required"`
		Type string `json:"type" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name, slug, and type are required")
		return
	}
	if req.Type != "desktop" && req.Type != "saas" && req.Type != "hybrid" {
		response.BadRequest(c, "type must be desktop, saas, or hybrid")
		return
	}
	if err := apperr.ValidateName("name", req.Name); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	if err := apperr.ValidateSlug(req.Slug); err != nil {
		response.BadRequest(c, err.Message)
		return
	}

	p := &model.Product{Name: req.Name, Slug: req.Slug, Type: req.Type}
	if err := h.Store.CreateProduct(c, p); err != nil {
		response.Err(c, http.StatusConflict, "DUPLICATE", "product slug already exists")
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "product", EntityID: p.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"name": req.Name, "slug": req.Slug, "type": req.Type},
	})

	response.Created(c, p)
}

func (h *AdminHandler) UpdateProduct(c *gin.Context) {
	p, err := h.Store.FindProductByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "product not found")
		return
	}

	var req struct {
		Name                    string  `json:"name"`
		Slug                    string  `json:"slug"`
		Type                    string  `json:"type"`
		MinimumSupportedVersion *string `json:"minimum_supported_version"`
		MinimumSupportedMessage *string `json:"minimum_supported_message"`
		RequireSigning          *bool   `json:"require_signing"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if req.Name != "" {
		if err := apperr.ValidateName("name", req.Name); err != nil {
			response.BadRequest(c, err.Message)
			return
		}
		p.Name = req.Name
	}
	if req.Slug != "" {
		if err := apperr.ValidateSlug(req.Slug); err != nil {
			response.BadRequest(c, err.Message)
			return
		}
		p.Slug = req.Slug
	}
	if req.Type != "" {
		if req.Type != "desktop" && req.Type != "saas" && req.Type != "hybrid" {
			response.BadRequest(c, "type must be desktop, saas, or hybrid")
			return
		}
		// Only validate against existing plans when the type is
		// actually changing. Re-saving the same type is a no-op.
		if req.Type != p.Type {
			n, err := h.Store.CountPlansIncompatibleWithType(c, p.ID, req.Type)
			if err != nil {
				response.Internal(c)
				return
			}
			if n > 0 {
				response.Err(c, http.StatusConflict, "INCOMPATIBLE_PLANS",
					fmt.Sprintf("%d existing plan(s) use fields that are not valid for %s products; reset max_activations / max_seats / license_model on those plans first",
						n, req.Type))
				return
			}
		}
		p.Type = req.Type
	}
	if req.MinimumSupportedVersion != nil {
		v := strings.TrimSpace(*req.MinimumSupportedVersion)
		if v != "" {
			if err := apperr.ValidateSemver(v); err != nil {
				response.BadRequest(c, err.Message)
				return
			}
		}
		p.MinimumSupportedVersion = v
	}
	if req.MinimumSupportedMessage != nil {
		msg := strings.TrimSpace(*req.MinimumSupportedMessage)
		if len(msg) > 1024 {
			response.BadRequest(c, "minimum_supported_message must be ≤ 1024 chars")
			return
		}
		p.MinimumSupportedMessage = msg
	}
	if req.RequireSigning != nil {
		p.RequireSigning = *req.RequireSigning
	}

	if err := h.Store.UpdateProduct(c, p); err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, p)
}

func (h *AdminHandler) DeleteProduct(c *gin.Context) {
	id := c.Param("id")
	count, _ := h.Store.ProductLicenseCount(c, id)
	if count > 0 {
		response.Err(c, http.StatusConflict, "HAS_LICENSES", "cannot delete product with existing licenses")
		return
	}
	if err := h.Store.DeleteProduct(c, id); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "product", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// ─── Plans ───

func (h *AdminHandler) ListPlans(c *gin.Context) {
	plans, err := h.Store.ListPlans(c, c.Query("product_id"), c.Query("search"))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"plans": plans})
}

func (h *AdminHandler) GetPlan(c *gin.Context) {
	p, err := h.Store.FindPlanByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}
	response.OK(c, p)
}

func (h *AdminHandler) CreatePlan(c *gin.Context) {
	var req struct {
		ProductID       string `json:"product_id" binding:"required"`
		Name            string `json:"name" binding:"required"`
		Slug            string `json:"slug" binding:"required"`
		LicenseType     string `json:"license_type" binding:"required"`
		BillingInterval string `json:"billing_interval"`
		MaxActivations  int    `json:"max_activations"`
		MaxSeats        int    `json:"max_seats"`
		TrialDays       int    `json:"trial_days"`
		GraceDays       int    `json:"grace_days"`
		StripePriceID   string `json:"stripe_price_id"`
		LicenseModel    string `json:"license_model"`
		FloatingTimeout int    `json:"floating_timeout"`
		SortOrder       int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "product_id, name, slug, and license_type are required")
		return
	}

	if err := apperr.ValidateName("name", req.Name); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	if err := apperr.ValidateSlug(req.Slug); err != nil {
		response.BadRequest(c, err.Message)
		return
	}

	switch req.LicenseType {
	case "subscription", "perpetual", "trial":
	default:
		response.BadRequest(c, "license_type must be subscription, perpetual, or trial")
		return
	}

	if err := validatePlanNumericBounds(planBounds{
		MaxActivations:  req.MaxActivations,
		MaxSeats:        req.MaxSeats,
		TrialDays:       req.TrialDays,
		GraceDays:       req.GraceDays,
		FloatingTimeout: req.FloatingTimeout,
	}); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if req.GraceDays <= 0 {
		req.GraceDays = 7
	}

	licenseModel := req.LicenseModel
	if licenseModel == "" {
		licenseModel = "standard"
	}
	if licenseModel != "standard" && licenseModel != "floating" {
		response.BadRequest(c, "license_model must be standard or floating")
		return
	}
	floatingTimeout := req.FloatingTimeout
	if floatingTimeout <= 0 {
		floatingTimeout = 30
	}

	// Product-type capability gating. The product decides WHAT a plan
	// can configure; price model (perpetual/subscription/trial) stays
	// orthogonal. Example: a saas product's plan must not set
	// max_activations (no per-device licensing). A desktop product's
	// plan must not set max_seats. Hybrid allows both.
	prod, err := h.Store.FindProductByID(c, req.ProductID)
	if err != nil {
		response.NotFound(c, "product not found")
		return
	}
	if req.MaxActivations > 0 && !model.ProductSupports(prod.Type, model.CapActivations) {
		response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
			"max_activations is not supported for "+prod.Type+" products (use a hybrid or desktop product)")
		return
	}
	if req.MaxSeats > 0 && !model.ProductSupports(prod.Type, model.CapSeats) {
		response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
			"max_seats is not supported for "+prod.Type+" products (use a hybrid or saas product)")
		return
	}
	// Default MaxActivations only when the product supports activations.
	// Otherwise leave it 0 (the column accepts zero and the runtime
	// guards prevent activation-family endpoints from being called).
	if req.MaxActivations <= 0 && model.ProductSupports(prod.Type, model.CapActivations) {
		req.MaxActivations = 3
	}
	if licenseModel == "floating" && !model.ProductSupports(prod.Type, model.CapActivations) {
		response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
			"floating license_model is not supported for "+prod.Type+" products")
		return
	}

	p := &model.Plan{
		ProductID:       req.ProductID,
		Name:            req.Name,
		Slug:            req.Slug,
		LicenseType:     req.LicenseType,
		BillingInterval: req.BillingInterval,
		MaxActivations:  req.MaxActivations,
		MaxSeats:        req.MaxSeats,
		TrialDays:       req.TrialDays,
		GraceDays:       req.GraceDays,
		StripePriceID:   req.StripePriceID,
		LicenseModel:    licenseModel,
		FloatingTimeout: floatingTimeout,
		Active:          true,
		SortOrder:       req.SortOrder,
	}
	if err := h.Store.CreatePlan(c, p); err != nil {
		response.Err(c, http.StatusConflict, "DUPLICATE", "plan slug already exists for this product")
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "plan", EntityID: p.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"name": req.Name, "product_id": req.ProductID},
	})

	response.Created(c, p)
}

func (h *AdminHandler) UpdatePlan(c *gin.Context) {
	p, err := h.Store.FindPlanByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}

	var req struct {
		Name            *string `json:"name"`
		Slug            *string `json:"slug"`
		LicenseType     *string `json:"license_type"`
		BillingInterval *string `json:"billing_interval"`
		MaxActivations  *int    `json:"max_activations"`
		MaxSeats        *int    `json:"max_seats"`
		TrialDays       *int    `json:"trial_days"`
		GraceDays       *int    `json:"grace_days"`
		StripePriceID   *string `json:"stripe_price_id"`
		LicenseModel    *string `json:"license_model"`
		FloatingTimeout *int    `json:"floating_timeout"`
		Active          *bool   `json:"active"`
		SortOrder       *int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	// Capability gating mirrors CreatePlan: validate against the parent
	// product's type. Look up the product once.
	prod, err := h.Store.FindProductByID(c, p.ProductID)
	if err != nil {
		response.Internal(c)
		return
	}
	if req.MaxActivations != nil && *req.MaxActivations > 0 && !model.ProductSupports(prod.Type, model.CapActivations) {
		response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
			"max_activations is not supported for "+prod.Type+" products")
		return
	}
	if req.MaxSeats != nil && *req.MaxSeats > 0 && !model.ProductSupports(prod.Type, model.CapSeats) {
		response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
			"max_seats is not supported for "+prod.Type+" products")
		return
	}
	if req.LicenseModel != nil {
		if *req.LicenseModel != "standard" && *req.LicenseModel != "floating" {
			response.BadRequest(c, "license_model must be standard or floating")
			return
		}
		if *req.LicenseModel == "floating" && !model.ProductSupports(prod.Type, model.CapActivations) {
			response.Err(c, http.StatusBadRequest, "INCOMPATIBLE_PRODUCT_TYPE",
				"floating license_model is not supported for "+prod.Type+" products")
			return
		}
	}
	// Mirror CreatePlan's license_type enum guard. Without this, an
	// UPDATE could persist arbitrary strings (e.g. "free", "pro") that
	// downstream branching (trial-only logic, billing routing) would
	// silently treat as the default branch.
	if req.LicenseType != nil {
		switch *req.LicenseType {
		case "subscription", "perpetual", "trial":
		default:
			response.BadRequest(c, "license_type must be subscription, perpetual, or trial")
			return
		}
	}

	// Numeric-range validation. Pull from the request when the
	// field was provided, otherwise the existing row value (so a
	// PATCH that doesn't touch a field doesn't accidentally reject
	// a legacy value).
	check := planBounds{
		MaxActivations: p.MaxActivations, MaxSeats: p.MaxSeats,
		TrialDays: p.TrialDays, GraceDays: p.GraceDays,
		FloatingTimeout: p.FloatingTimeout,
	}
	if req.MaxActivations != nil {
		check.MaxActivations = *req.MaxActivations
	}
	if req.MaxSeats != nil {
		check.MaxSeats = *req.MaxSeats
	}
	if req.TrialDays != nil {
		check.TrialDays = *req.TrialDays
	}
	if req.GraceDays != nil {
		check.GraceDays = *req.GraceDays
	}
	if req.FloatingTimeout != nil {
		check.FloatingTimeout = *req.FloatingTimeout
	}
	if err := validatePlanNumericBounds(check); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Slug != nil {
		p.Slug = *req.Slug
	}
	if req.LicenseType != nil {
		p.LicenseType = *req.LicenseType
	}
	if req.BillingInterval != nil {
		p.BillingInterval = *req.BillingInterval
	}
	if req.MaxActivations != nil {
		p.MaxActivations = *req.MaxActivations
	}
	if req.MaxSeats != nil {
		p.MaxSeats = *req.MaxSeats
	}
	if req.TrialDays != nil {
		p.TrialDays = *req.TrialDays
	}
	if req.GraceDays != nil {
		p.GraceDays = *req.GraceDays
	}
	if req.StripePriceID != nil {
		p.StripePriceID = *req.StripePriceID
	}
	if req.LicenseModel != nil {
		p.LicenseModel = *req.LicenseModel
	}
	if req.FloatingTimeout != nil {
		p.FloatingTimeout = *req.FloatingTimeout
	}
	if req.Active != nil {
		p.Active = *req.Active
	}
	if req.SortOrder != nil {
		p.SortOrder = *req.SortOrder
	}

	if err := h.Store.UpdatePlan(c, p); err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, p)
}

func (h *AdminHandler) DeletePlan(c *gin.Context) {
	id := c.Param("id")
	count, _ := h.Store.PlanLicenseCount(c, id)
	if count > 0 {
		response.Err(c, http.StatusConflict, "HAS_LICENSES", "cannot delete plan with existing licenses")
		return
	}
	if err := h.Store.DeletePlan(c, id); err != nil {
		response.Internal(c)
		return
	}
	response.NoContent(c)
}

// ─── Entitlements ───

func (h *AdminHandler) CreateEntitlement(c *gin.Context) {
	var req struct {
		PlanID               string `json:"plan_id" binding:"required"`
		Feature              string `json:"feature" binding:"required"`
		ValueType            string `json:"value_type" binding:"required"`
		Value                string `json:"value" binding:"required"`
		QuotaPeriod          string `json:"quota_period"`
		QuotaUnit            string `json:"quota_unit"`
		StripeMeterEventName string `json:"stripe_meter_event_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "plan_id, feature, value_type, and value are required")
		return
	}

	switch req.ValueType {
	case "bool", "int", "string", "quota", "flag":
	default:
		response.BadRequest(c, "value_type must be bool, int, string, quota, or flag")
		return
	}
	// Meter event names only make sense for quota features — they
	// describe how to bill incremental usage. Reject early so a
	// boolean feature doesn't silently carry a useless field.
	if req.StripeMeterEventName != "" && req.ValueType != "quota" {
		response.BadRequest(c, "stripe_meter_event_name requires value_type=quota")
		return
	}

	e := &model.Entitlement{
		PlanID: req.PlanID, Feature: req.Feature,
		ValueType: req.ValueType, Value: req.Value,
		QuotaPeriod: req.QuotaPeriod, QuotaUnit: req.QuotaUnit,
		StripeMeterEventName: req.StripeMeterEventName,
	}
	if err := h.Store.CreateEntitlement(c, e); err != nil {
		response.Err(c, http.StatusConflict, "DUPLICATE", "entitlement already exists for this plan and feature")
		return
	}
	response.Created(c, e)
}

func (h *AdminHandler) UpdateEntitlement(c *gin.Context) {
	e, err := h.Store.FindEntitlementByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "entitlement not found")
		return
	}
	var req struct {
		Feature              *string `json:"feature"`
		ValueType            *string `json:"value_type"`
		Value                *string `json:"value"`
		QuotaPeriod          *string `json:"quota_period"`
		QuotaUnit            *string `json:"quota_unit"`
		StripeMeterEventName *string `json:"stripe_meter_event_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if req.Feature != nil {
		e.Feature = *req.Feature
	}
	if req.ValueType != nil {
		e.ValueType = *req.ValueType
	}
	if req.Value != nil {
		e.Value = *req.Value
	}
	if req.QuotaPeriod != nil {
		e.QuotaPeriod = *req.QuotaPeriod
	}
	if req.QuotaUnit != nil {
		e.QuotaUnit = *req.QuotaUnit
	}
	if req.StripeMeterEventName != nil {
		// Same gate as CreatePlan: meter wiring requires quota
		// semantics. Comparing against the post-merge ValueType in
		// case the same request also flipped value_type.
		if *req.StripeMeterEventName != "" && e.ValueType != "quota" {
			response.BadRequest(c, "stripe_meter_event_name requires value_type=quota")
			return
		}
		e.StripeMeterEventName = *req.StripeMeterEventName
	}

	if err := h.Store.UpdateEntitlement(c, e); err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, e)
}

func (h *AdminHandler) DeleteEntitlement(c *gin.Context) {
	if err := h.Store.DeleteEntitlement(c, c.Param("id")); err != nil {
		response.Internal(c)
		return
	}
	response.NoContent(c)
}

// ─── API Keys ───

func (h *AdminHandler) ListAPIKeys(c *gin.Context) {
	keys, err := h.Store.ListAPIKeys(c, c.Query("product_id"), c.Query("search"))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"api_keys": keys})
}

func (h *AdminHandler) CreateAPIKey(c *gin.Context) {
	// product_id is OPTIONAL: leave empty for system-wide keys (used
	// with the `admin` scope for operator scripts / CI/CD that need
	// to manage all products). Per-product keys (future: licenses:read
	// etc.) set product_id explicitly.
	var req struct {
		ProductID string   `json:"product_id"`
		Name      string   `json:"name" binding:"required"`
		Scopes    []string `json:"scopes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name is required")
		return
	}
	if req.ProductID != "" {
		if _, err := h.Store.FindProductByID(c, req.ProductID); err != nil {
			response.NotFound(c, "product not found")
			return
		}
	}
	if err := apperr.ValidateName("name", req.Name); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	// Scope validation: reject typos at the boundary so an
	// unintentional `licneses:write` doesn't become a useless key
	// that's silently locked out of every route. Empty scopes are
	// allowed and produce a fail-closed key (caller may want to set
	// scopes later via rotate or by re-creating).
	if err := validateScopes(req.Scopes); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	rawKey := store.GenerateRawAPIKey()
	prefix := rawKey[:12]
	if req.Scopes == nil {
		req.Scopes = []string{}
	}

	ak := &model.APIKey{
		ProductID: req.ProductID,
		Name:      req.Name,
		Prefix:    prefix,
		Scopes:    req.Scopes,
	}
	if err := h.Store.CreateAPIKey(c, ak, rawKey); err != nil {
		response.Internal(c)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "api_key", EntityID: ak.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"name": req.Name, "product_id": req.ProductID, "scopes": req.Scopes},
	})

	response.Created(c, gin.H{
		"id":         ak.ID,
		"product_id": ak.ProductID,
		"name":       ak.Name,
		"key":        rawKey,
		"prefix":     prefix,
		"scopes":     ak.Scopes,
		"created_at": ak.CreatedAt,
	})
}

// planBounds carries the numeric fields that share the same range
// rules across CreatePlan + UpdatePlan, so both paths can share one
// validator. Zero is allowed everywhere (interpreted as "no limit"
// or "use the column default"); negative is always invalid; upper
// bounds are sized to "more than any plausible plan" so legitimate
// callers don't trip them.
type planBounds struct {
	MaxActivations  int
	MaxSeats        int
	TrialDays       int
	GraceDays       int
	FloatingTimeout int // minutes
}

func validatePlanNumericBounds(b planBounds) error {
	switch {
	case b.MaxActivations < 0:
		return fmt.Errorf("max_activations cannot be negative")
	case b.MaxActivations > 10000:
		return fmt.Errorf("max_activations cannot exceed 10000")
	case b.MaxSeats < 0:
		return fmt.Errorf("max_seats cannot be negative")
	case b.MaxSeats > 100000:
		return fmt.Errorf("max_seats cannot exceed 100000")
	case b.TrialDays < 0:
		return fmt.Errorf("trial_days cannot be negative")
	case b.TrialDays > 365:
		return fmt.Errorf("trial_days cannot exceed 365")
	case b.GraceDays < 0:
		return fmt.Errorf("grace_days cannot be negative")
	case b.GraceDays > 365:
		return fmt.Errorf("grace_days cannot exceed 365")
	case b.FloatingTimeout < 0:
		return fmt.Errorf("floating_timeout cannot be negative")
	case b.FloatingTimeout > 1440:
		return fmt.Errorf("floating_timeout cannot exceed 1440 minutes (1 day)")
	}
	return nil
}

// validateScopes rejects unknown scope strings. Keeping the check at
// the boundary catches typos that would otherwise produce a silently
// powerless key.
func validateScopes(scopes []string) error {
	for _, s := range scopes {
		if !model.IsValidScope(s) {
			return fmt.Errorf("unknown scope %q (valid: %v)", s, model.AllScopes())
		}
	}
	return nil
}

// RotateAPIKey generates a new secret for an existing key. The old
// secret stops working immediately — pre-launch we don't ship a
// grace period because rolling two valid secrets at once doubles the
// blast radius if either leaks during the rotation window.
//
// The key ID is stable so callers can update their config without
// touching the row's name / scopes / product_id.
func (h *AdminHandler) RotateAPIKey(c *gin.Context) {
	id := c.Param("id")
	ak, err := h.Store.FindAPIKeyByID(c, id)
	if err != nil {
		response.NotFound(c, "api key not found")
		return
	}
	rawKey := store.GenerateRawAPIKey()
	prefix := rawKey[:12]
	if err := h.Store.RotateAPIKey(c, ak.ID, rawKey, prefix); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "api_key", EntityID: ak.ID, Action: "rotated",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"name": ak.Name},
	})
	response.OK(c, gin.H{
		"id":     ak.ID,
		"name":   ak.Name,
		"key":    rawKey,
		"prefix": prefix,
		"scopes": ak.Scopes,
	})
}

func (h *AdminHandler) DeleteAPIKey(c *gin.Context) {
	id := c.Param("id")
	if err := h.Store.DeleteAPIKey(c, id); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "api_key", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// ─── Licenses ───

func (h *AdminHandler) ListLicenses(c *gin.Context) {
	productID := c.Query("product_id")
	// When the request comes from an API key bound to a specific
	// product, override (or fill in) the product_id filter so the
	// list can never leak rows from other products even when the
	// caller forgets — or deliberately omits — the query param.
	if v, ok := c.Get("api_key"); ok {
		if ak, ok := v.(*model.APIKey); ok && ak != nil && ak.ProductID != "" {
			if productID != "" && productID != ak.ProductID {
				response.Err(c, http.StatusForbidden, "PRODUCT_SCOPE_MISMATCH",
					"api_key is bound to a different product")
				return
			}
			productID = ak.ProductID
		}
	}
	licenses, total, err := h.Store.ListLicenses(c, store.LicenseListFilter{
		ProductID:           productID,
		Status:              c.Query("status"),
		Search:              c.Query("search"),
		ExternalCustomerID:  c.Query("external_customer_id"),
		ExternalWorkspaceID: c.Query("external_workspace_id"),
		Offset:              queryInt(c, "offset", 0),
		Limit:               queryInt(c, "limit", 50),
	})
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"licenses": licenses, "total": total})
}

func (h *AdminHandler) GetLicense(c *gin.Context) {
	id := c.Param("id")
	l, err := h.Store.FindLicenseByID(c, id)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}
	if !requireKeyProductScope(c, l.ProductID) {
		return
	}
	response.OK(c, l)
}

func (h *AdminHandler) CreateLicense(c *gin.Context) {
	var req struct {
		ProductID           string `json:"product_id" binding:"required"`
		PlanID              string `json:"plan_id" binding:"required"`
		Email               string `json:"email" binding:"required"`
		Notes               string `json:"notes"`
		ExternalCustomerID  string `json:"external_customer_id"`
		ExternalWorkspaceID string `json:"external_workspace_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "product_id, plan_id, and email are required")
		return
	}

	if appErr := apperr.ValidateEmail(req.Email); appErr != nil {
		response.BadRequest(c, appErr.Message)
		return
	}
	// External IDs are opaque to Keygate but we cap their length to
	// keep them index-friendly. 256 chars is comfortably above what
	// any real identifier scheme produces (UUIDs, Stripe IDs, etc).
	if len(req.ExternalCustomerID) > 256 {
		response.BadRequest(c, "external_customer_id must be ≤ 256 chars")
		return
	}
	if len(req.ExternalWorkspaceID) > 256 {
		response.BadRequest(c, "external_workspace_id must be ≤ 256 chars")
		return
	}

	// Look up plan first to determine license type and set appropriate fields
	plan, err := h.Store.FindPlanByID(c, req.PlanID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}
	// Plan/product consistency: a plan belongs to exactly one product
	// (plans.product_id is FK + NOT NULL). Accepting a mismatched
	// req.ProductID would persist a license whose product_id points
	// at a product whose plans don't include this one — that breaks
	// capability gating, billing routing, and every downstream join.
	if plan.ProductID != req.ProductID {
		response.Err(c, http.StatusBadRequest, "PLAN_PRODUCT_MISMATCH",
			"plan does not belong to the requested product")
		return
	}
	// API-key scoping: a key bound to product A can't mint a license
	// for product B even if it carries licenses:write.
	if !requireKeyProductScope(c, req.ProductID) {
		return
	}

	status := model.StatusActive
	if plan.LicenseType == "trial" {
		status = model.StatusTrialing
	}

	l := &model.License{
		ProductID:           req.ProductID,
		PlanID:              req.PlanID,
		Email:               req.Email,
		LicenseKey:          license.GenerateKey(""),
		Status:              status,
		Notes:               req.Notes,
		ExternalCustomerID:  req.ExternalCustomerID,
		ExternalWorkspaceID: req.ExternalWorkspaceID,
	}

	// Set valid_until for trial licenses
	if plan.LicenseType == "trial" && plan.TrialDays > 0 {
		until := time.Now().Add(time.Duration(plan.TrialDays) * 24 * time.Hour)
		l.ValidUntil = &until
	}

	// Create license and subscription in a single transaction to prevent orphan records
	if err := h.Store.CreateLicenseWithSubscription(c, l, plan); err != nil {
		response.Internal(c)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: l.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"email": req.Email, "plan_id": req.PlanID},
	})

	if h.Webhook != nil {
		h.Webhook.Dispatch(c, l.ProductID, "license.created", map[string]any{
			"license_id": l.ID, "email": req.Email, "plan_id": req.PlanID,
		})
	}

	// Email the customer their new license key. Best-effort: SMTP failure
	// is logged inside SendLicenseCreated but doesn't fail the API call —
	// the license is already persisted, and admin can resend manually.
	if h.Email != nil && h.Email.IsConfigured() {
		productName := ""
		if prod, err := h.Store.FindProductByID(c, l.ProductID); err == nil {
			productName = prod.Name
		}
		h.Email.SendLicenseCreated(req.Email, productName, plan.Name, l.LicenseKey)
	}

	response.Created(c, l)
}

func (h *AdminHandler) RevokeLicense(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	if err := h.Store.RevokeLicense(c, id); err != nil {
		response.NotFound(c, err.Error())
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "revoked",
		ActorType: "admin", ActorID: adminID(c),
	})
	if h.Webhook != nil {
		if lic, err := h.Store.FindLicenseByID(c, id); err == nil {
			h.Webhook.Dispatch(c, lic.ProductID, "license.revoked", map[string]any{
				"license_id": id, "email": lic.Email,
			})
		}
	}
	response.OK(c, gin.H{"status": "revoked"})
}

func (h *AdminHandler) RefundLicense(c *gin.Context) {
	id := c.Param("id")
	lic, err := h.Store.FindLicenseByID(c, id)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}
	if !requireKeyProductScope(c, lic.ProductID) {
		return
	}

	if lic.StripeSubscriptionID == "" {
		response.BadRequest(c, "license has no payment subscription to refund")
		return
	}

	// Cancel subscription at Stripe
	providerResult := "no_active_subscription"
	if _, cancelErr := subscription.Cancel(lic.StripeSubscriptionID, nil); cancelErr != nil {
		providerResult = "stripe_cancel_failed"
		slog.Error("stripe subscription cancel failed", "subscription_id", lic.StripeSubscriptionID, "error", cancelErr)
	} else {
		providerResult = "stripe_subscription_canceled"
	}

	// Mark the license as revoked
	lic.Status = model.StatusRevoked
	if err := h.Store.UpdateLicenseAndSubscription(c, lic, "status"); err != nil {
		response.Internal(c)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "refunded",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"provider_result": providerResult},
	})

	if h.Webhook != nil {
		h.Webhook.Dispatch(c, lic.ProductID, "license.revoked", map[string]any{
			"license_id": id, "email": lic.Email, "reason": "refund",
		})
	}

	response.OK(c, gin.H{"status": "refunded", "provider_result": providerResult})
}

func (h *AdminHandler) SuspendLicense(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	if err := h.Store.SuspendLicense(c, id); err != nil {
		response.NotFound(c, err.Error())
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "suspended",
		ActorType: "admin", ActorID: adminID(c),
	})
	if lic, err := h.Store.FindLicenseByID(c, id); err == nil {
		if h.Webhook != nil {
			h.Webhook.Dispatch(c, lic.ProductID, "license.suspended", map[string]any{
				"license_id": id, "email": lic.Email,
			})
		}
		if h.Email != nil && h.Email.IsConfigured() && lic.Email != "" {
			productName := ""
			if prod, perr := h.Store.FindProductByID(c, lic.ProductID); perr == nil {
				productName = prod.Name
			}
			h.Email.SendLicenseSuspended(lic.Email, productName, "")
		}
	}
	response.OK(c, gin.H{"status": "suspended"})
}

func (h *AdminHandler) ReinstateLicense(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	if err := h.Store.ReinstateLicense(c, id); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "reinstated",
		ActorType: "admin", ActorID: adminID(c),
	})
	if h.Webhook != nil {
		if lic, err := h.Store.FindLicenseByID(c, id); err == nil {
			h.Webhook.Dispatch(c, lic.ProductID, "license.reinstated", map[string]any{
				"license_id": id, "email": lic.Email,
			})
		}
	}
	response.OK(c, gin.H{"status": "active"})
}

func (h *AdminHandler) DeleteActivation(c *gin.Context) {
	id := c.Param("id")
	pid, err := h.Store.GetActivationProductID(c, id)
	if err != nil {
		response.NotFound(c, "activation not found")
		return
	}
	if !requireKeyProductScope(c, pid) {
		return
	}
	if err := h.Store.DeleteActivationByID(c, id); err != nil {
		response.Internal(c)
		return
	}
	response.NoContent(c)
}

// ─── Audit Logs ───

func (h *AdminHandler) ListAuditLogs(c *gin.Context) {
	logs, total, err := h.Store.ListAuditLogs(c,
		c.Query("entity"), c.Query("entity_id"), c.Query("product_id"),
		queryInt(c, "offset", 0), queryInt(c, "limit", 50))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"audit_logs": logs, "total": total})
}

// ─── Users ───

func (h *AdminHandler) ListUsers(c *gin.Context) {
	users, total, err := h.Store.ListUsers(c, c.Query("search"), queryInt(c, "offset", 0), queryInt(c, "limit", 50))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"users": users, "total": total})
}

// ─── Helpers ───

func adminID(c *gin.Context) string {
	v, _ := c.Get("user_id")
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func queryInt(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// checkLicenseScope is the per-handler entry point for API-key
// product scoping on license-id-bearing routes. Looks up the
// license's product_id and defers to requireKeyProductScope. Returns
// true on pass (caller continues); false after writing a 404 or 403
// (caller must `return`).
func (h *AdminHandler) checkLicenseScope(c *gin.Context, licenseID string) bool {
	pid, err := h.Store.GetLicenseProductID(c, licenseID)
	if err != nil {
		// sql.ErrNoRows is the dominant failure here; surface as 404.
		// Other DB errors are rare enough that 404 is still a safe
		// answer — we'd hit them again in the real handler.
		response.NotFound(c, "license not found")
		return false
	}
	return requireKeyProductScope(c, pid)
}

// requireKeyProductScope blocks an API key bound to product A from
// touching resources that belong to product B. Returns true to
// continue, false after writing a 403 (caller should `return`).
//
//   - session auth → true (admin dashboards span all products)
//   - api_key with empty product_id → true (system-wide key)
//   - api_key with matching product_id → true
//   - api_key with different product_id → 403 + false
//
// Call AFTER resolving the resource's product_id. Two-hop lookups
// (e.g. seat → license → product) just pass the final product_id
// here; this helper doesn't try to be clever.
func requireKeyProductScope(c *gin.Context, resourceProductID string) bool {
	v, ok := c.Get("api_key")
	if !ok {
		return true
	}
	ak, ok := v.(*model.APIKey)
	if !ok || ak == nil || ak.ProductID == "" {
		return true
	}
	if ak.ProductID == resourceProductID {
		return true
	}
	response.Err(c, http.StatusForbidden, "PRODUCT_SCOPE_MISMATCH",
		"api_key is bound to a different product")
	return false
}

// ─── Usage (admin) ───

func (h *AdminHandler) ListLicenseUsage(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	events, total, err := h.Store.ListUsageEvents(c, id, c.Query("feature"),
		queryInt(c, "offset", 0), queryInt(c, "limit", 50))
	if err != nil {
		response.Internal(c)
		return
	}
	counters, _ := h.Store.GetUsageSummary(c, id)
	response.OK(c, gin.H{"events": events, "counters": counters, "total": total})
}

func (h *AdminHandler) ResetLicenseUsage(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	var req struct {
		Feature   string `json:"feature" binding:"required"`
		Period    string `json:"period"`
		PeriodKey string `json:"period_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "feature is required")
		return
	}
	period := req.Period
	if period == "" {
		period = "monthly"
	}
	periodKey := req.PeriodKey
	if periodKey == "" {
		periodKey = store.CurrentPeriodKey(period)
	}
	if err := h.Store.ResetUsageCounter(c, id, req.Feature, period, periodKey); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "usage_reset",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"feature": req.Feature, "period": period, "period_key": periodKey},
	})
	response.OK(c, gin.H{"status": "reset"})
}

// ─── Seats (admin) ───

func (h *AdminHandler) ListLicenseSeats(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	seats, err := h.Store.ListSeats(c, id)
	if err != nil {
		response.Internal(c)
		return
	}
	count, _ := h.Store.CountActiveSeats(c, id)
	response.OK(c, gin.H{"seats": seats, "active_count": count})
}

// ─── Analytics (admin) ───

func (h *AdminHandler) ListAnalytics(c *gin.Context) {
	productID := c.Query("product_id")
	granularity := c.Query("granularity")
	var from, to time.Time
	if v := c.Query("from"); v != "" {
		from, _ = time.Parse("2006-01-02", v)
	}
	if v := c.Query("to"); v != "" {
		to, _ = time.Parse("2006-01-02", v)
	}
	if from.IsZero() {
		from = time.Now().AddDate(0, -1, 0)
	}
	if to.IsZero() {
		to = time.Now()
	}

	if granularity == "weekly" || granularity == "monthly" {
		snapshots, err := h.Store.ListAnalyticsSnapshotsAggregated(c, productID, from, to, granularity)
		if err != nil {
			response.Internal(c)
			return
		}
		response.OK(c, gin.H{"snapshots": snapshots, "granularity": granularity})
		return
	}

	snapshots, err := h.Store.ListAnalyticsSnapshots(c, productID, from, to)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"snapshots": snapshots})
}

func analyticsFilter(c *gin.Context) store.AnalyticsFilter {
	f := store.AnalyticsFilter{
		ProductID:   c.Query("product_id"),
		PlanID:      c.Query("plan_id"),
		LicenseType: c.Query("license_type"),
		Status:      c.Query("status"),
	}
	if v := c.Query("from"); v != "" {
		f.From, _ = time.Parse("2006-01-02", v)
	}
	if v := c.Query("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err == nil {
			// End-of-day: include the entire "to" date
			f.To = t.Add(24*time.Hour - time.Nanosecond)
		}
	}
	return f
}

func (h *AdminHandler) AnalyticsSummary(c *gin.Context) {
	summary, err := h.Store.GetAnalyticsSummary(c, analyticsFilter(c))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, summary)
}

func (h *AdminHandler) AnalyticsBreakdown(c *gin.Context) {
	dimension := c.Query("dimension")
	if dimension != "status" && dimension != "plan" && dimension != "license_type" {
		response.BadRequest(c, "dimension must be status, plan, or license_type")
		return
	}
	items, err := h.Store.GetLicenseBreakdown(c, analyticsFilter(c), dimension)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"items": items})
}

func (h *AdminHandler) AnalyticsUsageTop(c *gin.Context) {
	productID := c.Query("product_id")
	var from, to time.Time
	if v := c.Query("from"); v != "" {
		from, _ = time.Parse("2006-01-02", v)
	}
	if v := c.Query("to"); v != "" {
		to, _ = time.Parse("2006-01-02", v)
	}
	limit := queryInt(c, "limit", 10)
	features, err := h.Store.GetTopFeatureUsage(c, productID, from, to, limit)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"features": features})
}

func (h *AdminHandler) AnalyticsActivationTrend(c *gin.Context) {
	productID := c.Query("product_id")
	var from, to time.Time
	if v := c.Query("from"); v != "" {
		from, _ = time.Parse("2006-01-02", v)
	}
	if v := c.Query("to"); v != "" {
		to, _ = time.Parse("2006-01-02", v)
	}
	trend, err := h.Store.GetActivationTrend(c, productID, from, to)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"trend": trend})
}

func (h *AdminHandler) AnalyticsInsights(c *gin.Context) {
	f := analyticsFilter(c)
	growth, err := h.Store.GetGrowthMetrics(c, f.ProductID)
	if err != nil {
		response.Internal(c)
		return
	}
	ageDist, _ := h.Store.GetLicenseAgeDistribution(c, f.ProductID)
	topUsers, _ := h.Store.GetTopUsers(c, f.ProductID, queryInt(c, "top_limit", 10))
	retention, _ := h.Store.GetRetentionData(c, f.ProductID, queryInt(c, "months", 6))
	recentActivity, _ := h.Store.GetRecentActivity(c, f.ProductID, queryInt(c, "activity_limit", 20))

	response.OK(c, gin.H{
		"growth":           growth,
		"age_distribution": ageDist,
		"top_users":        topUsers,
		"retention":        retention,
		"recent_activity":  recentActivity,
	})
}

func (h *AdminHandler) GetUserDetail(c *gin.Context) {
	id := c.Param("id")
	detail, err := h.Store.GetUserDetail(c, id)
	if err != nil {
		response.NotFound(c, "user not found")
		return
	}
	response.OK(c, detail)
}

// ─── Addons ───

func (h *AdminHandler) ListAddons(c *gin.Context) {
	addons, err := h.Store.ListAddons(c, c.Query("product_id"), c.Query("search"))
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"addons": addons})
}

func (h *AdminHandler) CreateAddon(c *gin.Context) {
	var req struct {
		ProductID   string `json:"product_id" binding:"required"`
		Name        string `json:"name" binding:"required"`
		Slug        string `json:"slug" binding:"required"`
		Description string `json:"description"`
		Feature     string `json:"feature" binding:"required"`
		ValueType   string `json:"value_type" binding:"required"`
		Value       string `json:"value" binding:"required"`
		QuotaPeriod string `json:"quota_period"`
		QuotaUnit   string `json:"quota_unit"`
		SortOrder   int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "product_id, name, slug, feature, value_type, and value are required")
		return
	}
	if err := apperr.ValidateName("name", req.Name); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	if err := apperr.ValidateSlug(req.Slug); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	switch req.ValueType {
	case "bool", "int", "string", "quota":
	default:
		response.BadRequest(c, "value_type must be bool, int, string, or quota")
		return
	}

	a := &model.Addon{
		ProductID: req.ProductID, Name: req.Name, Slug: req.Slug,
		Description: req.Description, Feature: req.Feature,
		ValueType: req.ValueType, Value: req.Value,
		QuotaPeriod: req.QuotaPeriod, QuotaUnit: req.QuotaUnit,
		Active: true, SortOrder: req.SortOrder,
	}
	if err := h.Store.CreateAddon(c, a); err != nil {
		response.Err(c, 409, "DUPLICATE", "addon slug already exists for this product")
		return
	}
	response.Created(c, a)
}

func (h *AdminHandler) UpdateAddon(c *gin.Context) {
	a, err := h.Store.FindAddonByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "addon not found")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Feature     *string `json:"feature"`
		ValueType   *string `json:"value_type"`
		Value       *string `json:"value"`
		QuotaPeriod *string `json:"quota_period"`
		QuotaUnit   *string `json:"quota_unit"`
		Active      *bool   `json:"active"`
		SortOrder   *int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Name != nil {
		a.Name = *req.Name
	}
	if req.Description != nil {
		a.Description = *req.Description
	}
	if req.Feature != nil {
		a.Feature = *req.Feature
	}
	if req.ValueType != nil {
		a.ValueType = *req.ValueType
	}
	if req.Value != nil {
		a.Value = *req.Value
	}
	if req.QuotaPeriod != nil {
		a.QuotaPeriod = *req.QuotaPeriod
	}
	if req.QuotaUnit != nil {
		a.QuotaUnit = *req.QuotaUnit
	}
	if req.Active != nil {
		a.Active = *req.Active
	}
	if req.SortOrder != nil {
		a.SortOrder = *req.SortOrder
	}
	if err := h.Store.UpdateAddon(c, a); err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, a)
}

func (h *AdminHandler) DeleteAddon(c *gin.Context) {
	if err := h.Store.DeleteAddon(c, c.Param("id")); err != nil {
		response.Internal(c)
		return
	}
	response.NoContent(c)
}

func (h *AdminHandler) AddLicenseAddon(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	var req struct {
		AddonID string `json:"addon_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "addon_id is required")
		return
	}
	la := &model.LicenseAddon{LicenseID: id, AddonID: req.AddonID, Enabled: true}
	if err := h.Store.AddLicenseAddon(c, la); err != nil {
		response.Internal(c)
		return
	}
	response.Created(c, la)
}

func (h *AdminHandler) RemoveLicenseAddon(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	if err := h.Store.RemoveLicenseAddon(c, id, c.Param("addon_id")); err != nil {
		response.Internal(c)
		return
	}
	response.NoContent(c)
}

func (h *AdminHandler) ListLicenseAddons(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	addons, err := h.Store.ListLicenseAddons(c, id)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"addons": addons})
}

func (h *AdminHandler) ListFloatingSessions(c *gin.Context) {
	id := c.Param("id")
	if !h.checkLicenseScope(c, id) {
		return
	}
	sessions, err := h.Store.ListFloatingSessions(c, id)
	if err != nil {
		response.Internal(c)
		return
	}
	active, _ := h.Store.CountActiveFloating(c, id)
	response.OK(c, gin.H{"sessions": sessions, "active": active})
}

// ─── Change Plan (admin) ───

func (h *AdminHandler) ChangeLicensePlan(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		PlanID string `json:"plan_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "plan_id is required")
		return
	}

	l, err := h.Store.FindLicenseByID(c, id)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}
	if !requireKeyProductScope(c, l.ProductID) {
		return
	}

	plan, err := h.Store.FindPlanByID(c, req.PlanID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}
	if plan.ProductID != l.ProductID {
		response.BadRequest(c, "plan must belong to the same product")
		return
	}

	oldPlanID := l.PlanID
	l.PlanID = req.PlanID
	if err := h.Store.UpdateLicense(c, l, "plan_id"); err != nil {
		response.Internal(c)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: id, Action: "plan_changed",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"old_plan_id": oldPlanID, "new_plan_id": req.PlanID},
	})

	if h.Webhook != nil {
		h.Webhook.Dispatch(c, l.ProductID, "plan.changed", map[string]any{
			"license_id": id, "old_plan_id": oldPlanID, "new_plan_id": req.PlanID,
		})
	}

	if h.Email != nil && h.Email.IsConfigured() && l.Email != "" {
		productName := ""
		if prod, perr := h.Store.FindProductByID(c, l.ProductID); perr == nil {
			productName = prod.Name
		}
		oldPlanName := ""
		if oldPlan, perr := h.Store.FindPlanByID(c, oldPlanID); perr == nil {
			oldPlanName = oldPlan.Name
		}
		h.Email.SendPlanChanged(l.Email, productName, oldPlanName, plan.Name)
	}

	response.OK(c, gin.H{"status": "plan_changed", "plan_id": req.PlanID})
}

// ─── Settings ───

func (h *AdminHandler) GetSettings(c *gin.Context) {
	settings, err := h.Store.GetSettings(c)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"settings": settings})
}

func (h *AdminHandler) UpdateSettings(c *gin.Context) {
	var req struct {
		Settings map[string]string `json:"settings" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "settings map is required")
		return
	}

	// Validate allowed keys
	allowed := map[string]bool{
		"site_name": true, "timezone": true, "language": true, "brand_color": true, "logo_url": true,
		"smtp_host": true, "smtp_port": true, "smtp_username": true,
		"smtp_password": true, "smtp_from": true,
		"rate_limit_api": true, "rate_limit_admin": true,
		"webhook_max_attempts": true, "webhook_timeout": true,
		"quota_warning_threshold":          true,
		"setup_complete":                   true,
		"email_template_license_created":   true,
		"email_template_license_expiring":  true,
		"email_template_license_expired":   true,
		"email_template_trial_expired":     true,
		"email_template_license_suspended": true,
		"email_template_quota_warning":     true,
		"email_template_seat_invite":       true,
		"email_template_payment_failed":    true,
	}
	for key := range req.Settings {
		if !allowed[key] {
			response.BadRequest(c, "unknown setting: "+key)
			return
		}
	}

	if err := h.Store.SetSettings(c, req.Settings); err != nil {
		response.Internal(c)
		return
	}

	keys := make([]string, 0, len(req.Settings))
	for k := range req.Settings {
		keys = append(keys, k)
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "settings", EntityID: "system", Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"keys": keys},
	})

	response.OK(c, gin.H{"status": "saved"})
}

// RunMeteredSync triggers one drain pass of the Stripe meter-event
// queue on demand. Useful for operators who just attached an
// entitlement to a Stripe meter and want to ship the backlog
// immediately, and for end-to-end tests that don't want to wait
// for the 5-minute loop.
func (h *AdminHandler) RunMeteredSync(c *gin.Context) {
	if h.Metered == nil {
		response.Err(c, http.StatusServiceUnavailable, "METERED_SYNC_NOT_AVAILABLE",
			"metered billing syncer is not wired on this server")
		return
	}
	pushed := h.Metered.RunOnce(c.Request.Context(), 200)
	response.OK(c, gin.H{"pushed": pushed})
}

// RunExpiryChecks triggers one full pass of the lifecycle checker on
// demand. Useful for admins (apply new grace_days immediately, debug
// "did expiry mailer run last night?") and for end-to-end tests of the
// expiring / dunning / renewal email paths that are otherwise on an
// hourly cron.
func (h *AdminHandler) RunExpiryChecks(c *gin.Context) {
	if h.Expiry == nil {
		response.Err(c, http.StatusServiceUnavailable, "EXPIRY_NOT_AVAILABLE",
			"expiry checker is not wired on this server")
		return
	}
	h.Expiry.RunAll(c.Request.Context())
	response.OK(c, gin.H{"status": "ran"})
}

// SendTestEmail sends a real test message through the configured SMTP
// server so the admin can verify host/port/credentials end-to-end.
// Body: { "to": "user@example.com" } — optional; defaults to the
// logged-in admin's own email address.
func (h *AdminHandler) SendTestEmail(c *gin.Context) {
	if h.Email == nil || !h.Email.IsConfigured() {
		response.Err(c, http.StatusServiceUnavailable, "EMAIL_NOT_CONFIGURED",
			"SMTP is not configured on this server — set SMTP_HOST / SMTP_FROM / etc.")
		return
	}
	var req struct {
		To string `json:"to"`
	}
	_ = c.ShouldBindJSON(&req) // body optional
	to := strings.TrimSpace(req.To)
	if to == "" {
		if v, ok := c.Get("email"); ok {
			to, _ = v.(string)
		}
	}
	if err := apperr.ValidateEmail(to); err != nil {
		response.BadRequest(c, err.Message)
		return
	}
	if err := h.Email.Send(to,
		"Keygate test email",
		`<p>Hello! This is a test email from Keygate.</p>`+
			`<p>If you can read this, your SMTP setup is working.</p>`); err != nil {
		response.Err(c, http.StatusBadGateway, "EMAIL_SEND_FAILED", err.Error())
		return
	}
	response.OK(c, gin.H{"status": "sent", "to": to})
}

// GetEmailTemplates returns all email templates (custom from DB + hardcoded defaults).
func (h *AdminHandler) GetEmailTemplates(c *gin.Context) {
	defaults := service.DefaultTemplates()

	type templateInfo struct {
		Custom  string `json:"custom"`
		Default string `json:"default"`
	}

	result := make(map[string]templateInfo, len(defaults))
	for key, def := range defaults {
		custom, _ := h.Store.GetSetting(c, "email_template_"+key)
		result[key] = templateInfo{Custom: custom, Default: def}
	}

	response.OK(c, gin.H{"templates": result})
}

// ─── Team (Admin Management) ───

// ListTeamMembers returns all platform admins (owner + admin roles).
func (h *AdminHandler) ListTeamMembers(c *gin.Context) {
	admins, err := h.Store.ListAdmins(c)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"members": admins})
}

// InviteTeamMember promotes an existing user to admin, or creates a placeholder admin user.
// Only owners can invite new admins.
func (h *AdminHandler) InviteTeamMember(c *gin.Context) {
	// Only owner can manage team
	actorEmail, ok := c.Get("email")
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	actorUser, err := h.Store.FindUserByEmail(c, actorEmail.(string))
	if err != nil || actorUser.Role != model.RoleOwner {
		response.Forbidden(c, "only the owner can manage team members")
		return
	}

	var req struct {
		Email string `json:"email" binding:"required"`
		Role  string `json:"role"` // "admin" (default) or "owner"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "email is required")
		return
	}

	// Normalize before validation + lookups so case variants don't
	// create duplicate user rows or skip the self-invite guard.
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if appErr := apperr.ValidateEmail(req.Email); appErr != nil {
		response.BadRequest(c, appErr.Message)
		return
	}

	role := req.Role
	if role == "" {
		role = model.RoleAdmin
	}
	if role != model.RoleAdmin && role != model.RoleOwner {
		response.BadRequest(c, "role must be 'admin' or 'owner'")
		return
	}

	// Cannot invite yourself. EqualFold so "Owner@x.com" still trips
	// the guard even if the actor row was stored mixed-case.
	if strings.EqualFold(req.Email, actorUser.Email) {
		response.BadRequest(c, "cannot change your own role via invite")
		return
	}

	// Find or create user. Track whether THIS request changed
	// anything — if not, skip the notification email to avoid
	// spamming the invitee on repeated owner clicks.
	roleChanged := false
	user, err := h.Store.FindUserByEmail(c, req.Email)
	if err != nil {
		// User doesn't exist yet — create placeholder (will get proper name on first login)
		if err := h.Store.CreatePlaceholderUser(c, req.Email, role); err != nil {
			response.Internal(c)
			return
		}
		user, _ = h.Store.FindUserByEmail(c, req.Email)
		roleChanged = true
	} else {
		// User exists — check if already same role (idempotent)
		if user.Role == role {
			response.OK(c, user)
			return
		}
		if err := h.Store.SetUserRole(c, user.ID, role); err != nil {
			response.Internal(c)
			return
		}
		user.Role = role
		roleChanged = true
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "team", EntityID: user.ID, Action: "member_invited",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"email": req.Email, "role": role},
	})

	// Notify the new admin via email. Best-effort: a send failure
	// does NOT roll back the role grant (OTP login still works
	// out-of-band). The inviter (actor) name is shown so the
	// recipient knows who added them, which helps spot social
	// engineering ("why am I suddenly admin on Keygate?").
	if roleChanged && h.Email != nil && h.Email.IsConfigured() {
		siteName, _ := h.Store.GetSetting(c, "site_name")
		if siteName == "" {
			siteName = "Keygate"
		}
		baseURL, _ := h.Store.GetSetting(c, "base_url")
		if baseURL == "" {
			baseURL = adminBaseURL(c)
		}
		loginURL := strings.TrimRight(baseURL, "/") + "/login"
		inviter := actorUser.Name
		if inviter == "" {
			inviter = actorUser.Email
		}
		h.Email.SendAdminInvite(req.Email, siteName, inviter, role, loginURL)
	}

	response.OK(c, user)
}

// adminBaseURL infers the public base URL from the inbound request
// when the settings table doesn't have an explicit BASE_URL. Used
// only as a fallback for the admin invite email's login link.
func adminBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := c.Request.Host
	if forwarded := c.GetHeader("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

// RemoveTeamMember demotes an admin back to regular user.
// Only owners can remove admins. Cannot remove the last owner.
func (h *AdminHandler) RemoveTeamMember(c *gin.Context) {
	actorEmail, ok := c.Get("email")
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	actorUser, err := h.Store.FindUserByEmail(c, actorEmail.(string))
	if err != nil || actorUser.Role != model.RoleOwner {
		response.Forbidden(c, "only the owner can manage team members")
		return
	}

	targetID := c.Param("id")

	// Can't remove yourself
	if targetID == actorUser.ID {
		response.BadRequest(c, "cannot remove yourself from the team")
		return
	}

	target, err := h.Store.FindUserByID(c, targetID)
	if err != nil {
		response.NotFound(c, "user not found")
		return
	}

	if !target.IsAdmin() {
		response.BadRequest(c, "user is not a team member")
		return
	}

	// Atomic demote: a SELECT … FOR UPDATE inside DemoteOwnerAtomic
	// serialises concurrent demotion attempts so the last-owner
	// invariant holds even under racing requests. The non-atomic
	// "count then update" pattern that lived here previously could
	// be tricked into zero owners by two simultaneous calls.
	if err := h.Store.DemoteOwnerAtomic(c, targetID); err != nil {
		if errors.Is(err, store.ErrLastOwner) {
			response.BadRequest(c, "cannot remove the last owner")
			return
		}
		response.Internal(c)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "team", EntityID: targetID, Action: "member_removed",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"email": target.Email, "previous_role": target.Role},
	})

	response.OK(c, gin.H{"status": "removed"})
}

// ExportLicenses exports all licenses as CSV or JSON.
// GET /api/v1/admin/licenses/export?format=csv&product_id=xxx&status=xxx
func (h *AdminHandler) ExportLicenses(c *gin.Context) {
	format := c.DefaultQuery("format", "csv")
	if format != "csv" && format != "json" {
		response.BadRequest(c, "format must be csv or json")
		return
	}

	licenses, err := h.Store.ExportLicenses(c, c.Query("product_id"), c.Query("status"))
	if err != nil {
		response.Internal(c)
		return
	}

	dateStr := time.Now().Format("2006-01-02")

	if format == "json" {
		filename := fmt.Sprintf("licenses-%s.json", dateStr)
		c.Header("Content-Type", "application/json")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

		type exportLicense struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			Product    string `json:"product"`
			Plan       string `json:"plan"`
			Status     string `json:"status"`
			LicenseKey string `json:"license_key"`
			ValidFrom  string `json:"valid_from"`
			ValidUntil string `json:"valid_until"`
			CreatedAt  string `json:"created_at"`
		}

		out := make([]exportLicense, 0, len(licenses))
		for _, l := range licenses {
			productName := ""
			if l.Product != nil {
				productName = l.Product.Name
			}
			planName := ""
			if l.Plan != nil {
				planName = l.Plan.Name
			}
			validUntil := ""
			if l.ValidUntil != nil {
				validUntil = l.ValidUntil.Format(time.RFC3339)
			}
			out = append(out, exportLicense{
				ID:         l.ID,
				Email:      l.Email,
				Product:    productName,
				Plan:       planName,
				Status:     l.Status,
				LicenseKey: h.Store.DecryptLicenseKey(l),
				ValidFrom:  l.ValidFrom.Format(time.RFC3339),
				ValidUntil: validUntil,
				CreatedAt:  l.CreatedAt.Format(time.RFC3339),
			})
		}

		enc := json.NewEncoder(c.Writer)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			slog.Error("export json encode", "error", err)
		}
		return
	}

	// CSV format
	filename := fmt.Sprintf("licenses-%s.csv", dateStr)
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	w := csv.NewWriter(c.Writer)
	defer w.Flush()

	_ = w.Write([]string{"id", "email", "product", "plan", "status", "license_key", "valid_from", "valid_until", "created_at"})

	for _, l := range licenses {
		productName := ""
		if l.Product != nil {
			productName = l.Product.Name
		}
		planName := ""
		if l.Plan != nil {
			planName = l.Plan.Name
		}
		validUntil := ""
		if l.ValidUntil != nil {
			validUntil = l.ValidUntil.Format(time.RFC3339)
		}
		_ = w.Write([]string{
			l.ID,
			l.Email,
			productName,
			planName,
			l.Status,
			h.Store.DecryptLicenseKey(l),
			l.ValidFrom.Format(time.RFC3339),
			validUntil,
			l.CreatedAt.Format(time.RFC3339),
		})
	}
}
