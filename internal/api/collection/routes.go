package collection

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Routes registers the saved-item-scoped collection endpoints.
//
// The path is nested under /saved-items because the resource being acted on is a
// saved item: it is what gets filed into a collection. The router is supplied by
// the module, which mounts this feature under the existing /saved-items prefix.
//
// Auth is applied per route with middleware.Auth, never router.Use, so every
// route here is deliberate about requiring a Bearer token.
func Routes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	router.Put(
		"/:id/collection",
		middleware.Auth(verifier),
		handler.PutSavedItem,
	)
}

// CollectionRoutes registers the endpoints that act on a collection as its own
// resource.
//
// This is a separate function rather than another path in Routes because the two
// act on different resources and are mounted under different prefixes. Routes
// belongs to the /saved-items group, where every path is about a saved item;
// adding /:id there would collide with DELETE /saved-items/:id, which the saved
// item feature already owns and which deletes a saved item rather than a
// collection. Registering the collection delete here, on its own router, is what
// keeps the two operations from shadowing each other and keeps each resource's
// paths under one prefix.
//
// Auth is applied per route here too, never router.Use.
func CollectionRoutes(
	router fiber.Router,
	handler *Handler,
	verifier *security.AccessTokenVerifier,
) {
	// "/" rather than "": this group is mounted at /collections, so "/" is the
	// collection list itself and "/:id" is one collection. Registering the root path
	// as "" would leave the list unreachable.
	router.Get(
		"/",
		middleware.Auth(verifier),
		handler.ListCollections,
	)

	// "/:id/saved-items" rather than a nested router, so the collection remains the
	// resource being addressed and this reads as a view of one collection rather than
	// as a second resource that happens to hang off it.
	router.Get(
		"/:id/saved-items",
		middleware.Auth(verifier),
		handler.ListSavedItemsInCollection,
	)

	router.Delete(
		"/:id",
		middleware.Auth(verifier),
		handler.DeleteCollection,
	)
}
