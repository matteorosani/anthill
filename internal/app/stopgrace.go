package app

import "time"

// StopGrace is how long a run that Anthill stops has to exit before its
// process tree is killed. It leaves room to end the run within the 10
// seconds of spec ant-execution, "Stopping an ant".
const StopGrace = 5 * time.Second
