package servicereview

import (
	reviewlogic "console.eigenflux.ai/internal/servicereview"
	servicereview "console.eigenflux.ai/model/eigenflux/servicereview"
	"context"
	"github.com/cloudwego/hertz/pkg/app"
)

var serviceReviews *reviewlogic.Handler

func InitServiceReviewClient(client reviewlogic.Client) {
	serviceReviews = reviewlogic.NewHandler(client)
}

type ServiceReviewEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty" swaggertype:"object"`
}

// ListServiceReviews lists frozen service submissions for private operator review.
// @Summary List service moderation reviews
// @Tags Service Review
// @Produce json
// @Param status query string false "Moderation status" default(review)
// @Param cursor query string false "Decimal-string review ID cursor" default(0)
// @Param limit query int false "Page size (1–100)" default(20)
// @Success 200 {object} ServiceReviewEnvelope
// @Failure 400,503 {object} ServiceReviewEnvelope
// @Router /console/api/v1/service-reviews [get]
func ListServiceReviews(ctx context.Context, c *app.RequestContext) { serviceReviews.List(ctx, c) }

type ResolveServiceReviewBody struct {
	Decision string `json:"decision" enums:"pass,reject"`
	Operator string `json:"operator"`
	Reason   string `json:"reason"`
}

// ResolveServiceReview records an operator decision and seller-visible guidance.
// @Summary Resolve a service moderation review
// @Tags Service Review
// @Accept json
// @Produce json
// @Param id path string true "Decimal-string review ID"
// @Param body body ResolveServiceReviewBody true "Decision, operator identity and seller-visible reason"
// @Success 200 {object} ServiceReviewEnvelope
// @Failure 400,403,404,409,503 {object} ServiceReviewEnvelope
// @Router /console/api/v1/service-reviews/{id}/resolve [post]
func ResolveServiceReview(ctx context.Context, c *app.RequestContext) { serviceReviews.Resolve(ctx, c) }

// Keep the generated request contract referenced across hz updates.
var _ = servicereview.NewListServiceReviewsReq
