package main

// stageRefusal carries a finite internal stage code, never a driver error,
// SQL text, connection string, plan content or credential.
type stageRefusal struct{ code string }

func (e *stageRefusal) Error() string { return "worker bootstrap precondition refused" }
func refuseStage(code string) error   { return &stageRefusal{code: code} }
