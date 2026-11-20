package feedback

// This file is intentionally empty of codes.
//
// The only error this feature raises that is not already covered by apperror and the
// shared middleware codes is a validation failure on the message, the type or the
// client context, and those are reported as VALIDATION_ERROR with the offending field
// named. There is no maintenance surface, so there is no secret code to report.
//
// The file is kept rather than deleted because every feature in this project states
// its own codes explicitly, and a reader looking for them should find the explanation
// where the list would be rather than conclude one was forgotten.
