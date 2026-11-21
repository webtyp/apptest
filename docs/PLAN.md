# apptest — historias de usuario de punta a punta

Master: `webtyp/docs/TEST_DOMAIN_MASTER_PLAN.md` (D6–D11). Ejecución **local** (no hay ejecutor remoto); el dueño
revisa este plan **antes** de escribir código (🔵 en la fila E2 del master).

## Qué resuelve

Una historia («el doctor ve su agenda del día y elige una confirmada») se prueba hoy a mano por el MCP. `apptest`
la corre sola: el **servidor real** de la app (su `web/server` generado), una **base nueva** del motor de la app,
el **sitio real** (`sitec`, wasm incluido) y **Chrome sin ventana**. Sin mocks, sin SQL a mano, sin repetir en cada
app el código de la historia: la historia vive en su módulo (D6) y la app solo la lista.

## Ecosystem search

- `appkit/backend.Build` arma el servidor; **no se llama directo**: se compila el `web/server/main.go` generado
  (`appkitc`), el mismo binario de producción, con `-tags dev`. **Reusar.**
- `migrate/main.go` generado migra con `DATABASE_URL`. **Reusar** (`go run ./migrate`).
- `sitec.Build(rootDir, outDir)` compila hoja de estilos, scripts, shell HTML y wasm. **Reusar.**
- `webtyp.com/devbrowser/chromedp` (chromedp dentro de devbrowser) maneja Chrome. **Reusar**; `devbrowser` en sí
  no (es la ventana del desarrollador, con UI y MCP).
- `postgres/backup.go:76-85` ya crea una base con nombre único y su `drop` (`DROP … WITH (FORCE)`). **Extender**:
  sacar esa parte a `postgres.NewEmptyDatabase(dsn) (testDSN string, drop func() error, err error)`.
- Sesión: `auth` `DEV_AUTOLOGIN` (`auth/user.go:347`, `auth/authority/devlogin.go`) abre una sesión real por
  credencial, una por host (`localhost`, `user2.localhost`…). **Reusar**: una `Session` = un host. El primer
  administrador: la ruta de instalación del modo (`trusted_ip.WithFirstAdminSetup`, `auth.PathSetup`).
- Datos previos por HTTP: `rpc.Caller` (isomórfico, sobre `fetch`). `fetch` nativo no guarda cookie de sesión
  (`fetch/client_stdlib.go`, sin `Jar`). **Extender** `fetch`: opción de cookie jar en el cliente nativo.
- `wasmbrowsertest` corre pruebas *dentro* del navegador: no sirve, una historia necesita dos sesiones a la vez.
- **Nuevo**: `webtyp/apptest` (une lo anterior); nada en el ecosistema lo hace.

## Design gate

**1. Prior art** (leídos 2026-11-20):
- Playwright — [fixtures](https://playwright.dev/docs/test-fixtures), [auth](https://playwright.dev/docs/auth):
  un «browser context» aislado por usuario; varios roles en una prueba = varios contexts; el estado previo se arma
  por UI o por API (`request.post` + `storageState`), nunca por la base.
- Rails — [system testing](https://guides.rubyonrails.org/testing.html#system-testing): Capybara + Chrome sin
  ventana (`driven_by :selenium, using: :headless_chrome`); captura de pantalla automática al fallar.
- Laravel Dusk — [docs](https://laravel.com/docs/dusk): prohíbe la base en memoria y el rollback por transacción
  («should never use the `RefreshDatabase` trait»); migra o trunca una base real; varias sesiones = varios `Browser`
  en el mismo cierre (ejemplo de chat); `loginAs` para entrar sin formulario.
- Coincidimos con los tres: navegador real, un contexto por usuario, captura al fallar. Nos apartamos en dos: base
  **nueva por corrida** (no truncar: dos corridas no se pisan y no hay estado heredado) y la historia **vive en el
  módulo** que la cumple, no en la app (cada app la repetiría: regla del dueño).

**2. Nombres (prueba del novato):** `apptest.Story[G]` (una historia que pide los datos previos `G`), `Story.ID` (`H-ENCOUNTER-01`, D11),
`Story.Says` (la frase de la historia), `Story.Run`; `apptest.RunningApp` (la app ya levantada que recibe la historia); `apptest.Stories[G]` (las historias de un módulo, que piden
un `G`); `Stories.UsingGiven(g)` (las deja listas con los datos previos que entrega la app); `apptest.Run(t, …)`;
`RunningApp.SignedInAs(user)` (devuelve una `Session`), `Session.Open(screenID)`, `Session.Find(class)`, `Element.Click()`, `.Type(text)`,
`.ExpectCount(n)`, `.ExpectText(s)`, `.ExpectVisible()`, `.ExpectChoosable(bool)`. Clases tipadas
(`widget.Class`, p. ej. `selectsearch.ClsSsOption`), nunca un selector escrito a mano.

**3. Complejidad:** +1 repo (`apptest`), +1 función en `postgres`, +1 opción en `fetch`, +1 flag en `gotest`.
La app escribe un archivo de 5 líneas; el módulo escribe sus historias y una interfaz `Given`.

**4. Dónde:** `apptest` es solo de pruebas (`!wasm`, depende de Chrome y del compilador): no entra en `appkit`
(isomórfico) ni en ninguna librería de producción. Las historias, en `stories/` del módulo dueño.

**5. Qué borra:** las reservas `test-agenda-*` insertadas a mano y la verificación manual por MCP de cada arreglo.

## Cómo se ve (consumidor)

```go
// clinical_encounter/stories/given.go — lo que las historias necesitan que ya exista (sin importar otro módulo)
type Given interface {
	Doctor(name string) apptest.User                       // profesional con equipo registrado
	ConfirmedReservationToday(doctor apptest.User, patient string) (reservationID string)
	PendingReservationToday(doctor apptest.User, patient string) (reservationID string)
}
// clinical_encounter/stories/h_encounter_01_agenda.go
var AgendaShowsTodayChoosesConfirmed = apptest.Story[Given]{ID: "H-ENCOUNTER-01",
	Says: "El doctor ve solo sus reservas de hoy; la pendiente se ve y no se elige",
	Run: func(app *apptest.RunningApp, g Given) {
		doc := g.Doctor("doctor-a")
		g.ConfirmedReservationToday(doc, "ana"); g.PendingReservationToday(doc, "luis")
		ses := app.SignedInAs(doc).Open(ui.ID)
		ses.Find(selectsearch.ClsSsOption).ExpectCount(2)
		ses.Find(selectsearch.ClsSsOption).WithText("luis").ExpectChoosable(false)
	}}
// clinical_encounter/stories/all.go
var All = apptest.Stories[Given]{AgendaShowsTodayChoosesConfirmed /* , … */}

// mjosefa-cms/tests/stories_test.go — lo único que escribe la app
//go:build stories
func TestStories(t *testing.T) {
	apptest.Run(t, "..", clinicalstories.All.UsingGiven(given.Clinical{}), bookingstories.All.UsingGiven(given.Booking{}))
}
```
(`given.Clinical` lo arma la app con los ayudantes «Dado» que exportan `staff_manager`, `client_directory` y
`appointment_booking` en sus propios `stories/`.)

## Etapas (cada una deja `gotest` verde)

| # | Repo | Qué | Verificación |
|---|---|---|---|
| 1 | `postgres` | `NewEmptyDatabase(dsn)` sacado de `backup.go`; `backup.go` lo usa | prueba con DSN de entorno (se salta sin él) |
| 2 | `fetch` | cliente nativo con cookie jar (opción) | prueba contra `fetch/testserver` |
| 3 | `apptest` | `Run` arma la corrida: lee `DATABASE_URL` (`env.Get`, keyring del proyecto) → `NewEmptyDatabase` → `go run ./migrate` → `sitec.Build` a dir temporal → `go build -tags dev ./web/server` → arranca con `server_port` libre, `server_public_dir`, `DATABASE_URL`, `DEV_AUTOLOGIN`; espera `/health`; al terminar: para el servidor y `drop` | prueba con una app mínima en `apptest/tests/app/` |
| 4 | `apptest` | primer administrador por `auth.PathSetup` (código leído del log del servidor); `User` = credencial en `DEV_AUTOLOGIN`, host propio | idem |
| 5 | `apptest` | `Session` sobre `devbrowser/chromedp` (un contexto por host); `Find/Click/Type/Expect*` con espera de hasta 5 s; al fallar: captura PNG + errores de consola en `tests/stories_failures/<ID>.png` | idem |
| 6 | `apptest` | `Story`, `Stories[G]`, `UsingGiven`; cada historia = subprueba `TestStories/<ID>` | idem |
| 7 | `devflow` | `gotest -stories`: `go test -tags stories -json ./tests/`, escribe la columna «Estado» de `docs/TESTS.md` (✅ / ❌ + primera línea + fecha) por id | prueba de `gotest` con salida `-json` grabada |
| 8 | `clinical_encounter`, `appointment_booking`, `staff_manager`, `client_directory`, `mjosefa-cms` | piloto H-ENCOUNTER-01…04 y sus «Dado»; `mjosefa-cms/tests/stories_test.go`; filas en `docs/TESTS.md` | `gotest -stories` en mjosefa-cms; lo que falle queda ❌ con su porqué y se arregla en la librería dueña |
| 9 | `devskills` | **testing** (historias), **framework-map** (`apptest`); quitar STATUS | `devskills -f` |
