package appix

import (
	"log"
	"path"
	"path/filepath"
	"time"

	"github.com/Travix-International/appix/appixLogger"
	"github.com/Travix-International/appix/config"
	"github.com/rjeczalik/notify"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/Travix-International/appix/livereload"
)

// This watcher implements a simple state machine, making sure we handle currently if change events come in while we are executing a push.
//
// NOTE: The file watcher libraries sometimes send two separate events for one file change in quick succession. (Also, some editors, like vim, are doing multiple genuine file modifications for one single file save.)
// To mitigate this we initially wait for a short while befor starting the push, to make sure we are not pushing twice for a single change. That's why we have the initialDelay state.
//
//                              file change event
// initial state                     received
//   -------------> WAITING ------------------------> INITIAL_DELAY
//                     Λ                                    |
//                     |                                    | 100ms passed, executing push
//                     |                                    |
//                     |          push completed            V
//                      -------------------------------- PUSHING
//                                                        Λ   |
//                                         push completed |   | file change event received
//                                     execute a new push |   |
//                                                        |   V
//                                                 PUSHING_AND_GOT_EVENT
//
const (
	waiting            = iota
	initialDelay       = iota
	pushing            = iota
	pushingAndGotEvent = iota
)

var (
	appPath      string
	noBrowser    bool
	timeout      int
	watcherState = waiting
)

// RegisterWatch registers the 'watch' command.
func RegisterWatch(app *kingpin.Application, config config.Config, args *GlobalArgs, logger *appixLogger.Logger) {
	var localFrontend bool

	command := app.Command("watch", "Watches the current directory for changes, and pushes on any change.").
		Action(func(parseContext *kingpin.ParseContext) error {
			// Channel on which we get file change events.
			fileWatch := make(chan notify.EventInfo)
			// Channel on which we get an event when the initial short delay after a change is passed.
			initialDelayDone := make(chan int)
			// Channel on which we get events when the pushes are done.
			pushDone := make(chan int)

			// NOTE: We need to convert to absolute path, because the file watcher wouldn't accept relative paths on Windows.
			absPath, err := filepath.Abs(appPath)

			if err != nil {
				log.Fatal(err)
			}

			if err := notify.Watch(path.Join(absPath, "..."), fileWatch, notify.All); err != nil {
				log.Fatal(err)
			}

			defer notify.Stop(fileWatch)

			livereload.StartServer()

			// Immediately push once, and then start watching.
			doPush(config, args, true, localFrontend, nil, logger)

			livereload.SendReload()

			// Infinite loop, the user can exit with Ctrl+C.
			for {
				select {
				case ei := <-fileWatch:
					if args.Verbose {
						log.Println("File change event details:", ei)
					}

					filePath := ei.Path()
					relPath, err := filepath.Rel(absPath, filePath)

					if err != nil {
						log.Printf("Error obtaining relative file path to %s\n", filePath)
						break
					}

					if ignored := IgnoreFilePath(relPath); ignored {
						if args.Verbose {
							log.Println("Ignoring file changes:", filePath)
						}
						break
					}

					if watcherState == waiting {
						watcherState = initialDelay

						time.AfterFunc(100*time.Millisecond, func() {
							initialDelayDone <- 0
						})
					} else if watcherState == pushing {
						watcherState = pushingAndGotEvent
					}
				case <-initialDelayDone:
					watcherState = pushing

					log.Println("File change detected, executing appix push.")

					go doPush(config, args, false, localFrontend, pushDone, logger)
				case <-pushDone:
					if watcherState == pushingAndGotEvent {
						// A change event arrived while the previous push was happening, we push again.
						watcherState = pushing
						go doPush(config, args, false, localFrontend, pushDone, logger)
					} else {
						watcherState = waiting
						log.Println("Push done, watching for file changes.")
					}
				}
			}
		})

	command.Arg("appPath", "path to the App folder (default: current folder)").
		Default(".").
		ExistingDirVar(&appPath)

	command.Flag("noBrowser", "Appix won't open the frontend in the browser after every push.").
		Default("false").
		BoolVar(&noBrowser)

	command.Flag("local", "Upload to the local RWD frontend instead of the one returned by the catalog.").
		BoolVar(&localFrontend)

	command.Flag("timeout", "Set the maximum timeout for the request").
		Default("10").
		IntVar(&timeout)
}

func doPush(config config.Config, args *GlobalArgs, openBrowser bool, localFrontend bool, pushDone chan<- int, logger *appixLogger.Logger) {
	push(config, appPath, !openBrowser, 180, timeout, localFrontend, args, logger)

	if !openBrowser {
		livereload.SendReload()
	}

	if pushDone != nil {
		pushDone <- 0
	}
}
