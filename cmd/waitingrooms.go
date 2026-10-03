package cmd

var waitingRoomCols = []col{{"ID", "id"}, {"Name", "name"}, {"Host", "host"}, {"Path", "path"}, {"Suspended", "suspended"}, {"New users/min", "new_users_per_minute"}, {"Active users", "total_active_users"}}

var waitingRoomsCmd = group("waiting-rooms", "Waiting rooms", `Waiting Rooms queue visitors when traffic exceeds what an origin can take.

Examples:
  cfctl waiting-rooms list example.com
  cfctl waiting-rooms get example.com <id>
  cfctl waiting-rooms status example.com <id>
  cfctl waiting-rooms events example.com <id>
  cfctl waiting-rooms delete example.com <id>`, []string{"waiting-room", "waitingrooms"},
	readSpec{Use: "list <zone>", Short: "List waiting rooms", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms", Title: "Waiting rooms", Cols: waitingRoomCols, Paginate: true, Feature: "Waiting Rooms"}.build(),
	readSpec{Use: "get <zone> <id>", Short: "Show a waiting room", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms/{waiting_room_id}", Args: []string{"waiting_room_id"}, Title: "Waiting room", Feature: "Waiting Rooms"}.build(),
	readSpec{Use: "status <zone> <id>", Short: "Show live queue status", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms/{waiting_room_id}/status", Args: []string{"waiting_room_id"}, Title: "Status", Feature: "Waiting Rooms"}.build(),
	readSpec{Use: "events <zone> <id>", Short: "List scheduled events", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms/{waiting_room_id}/events", Args: []string{"waiting_room_id"}, Title: "Events", Feature: "Waiting Rooms",
		Cols: []col{{"ID", "id"}, {"Name", "name"}, {"Start", "event_start_time"}, {"End", "event_end_time"}, {"Suspended", "suspended"}}}.build(),
	writeSpec{Use: "create <zone>", Short: "Create a waiting room (--data JSON)", Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms", DataFlag: true, Done: "Waiting room created", Feature: "Waiting Rooms"}.build(),
	writeSpec{Use: "delete <zone> <id>", Short: "Delete a waiting room", Method: "DELETE", Scope: scopeZone, Path: "/zones/{zone_id}/waiting_rooms/{waiting_room_id}", Args: []string{"waiting_room_id"}, Confirm: "delete waiting room %s", Done: "Waiting room %s deleted", Feature: "Waiting Rooms"}.build(),
)

func init() { rootCmd.AddCommand(waitingRoomsCmd) }
