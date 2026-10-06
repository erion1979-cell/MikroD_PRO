# Adding a Power/UPS brand or model

Every model MikroDash can read is **one JSON file**. No program code changes. The file says which
Modbus registers to read and what each one means, and the Add unit form builds its **Brand** and
**Model** lists from these files.

```
internal/power/model/defs/
  powerguard/                 <- one folder per brand
    modbus-v1.1.json          <- the brand's register map (also "Other model")
    hp-10212.json             <- a product: its name, its details, and "uses" the map
  <your-brand>/
    <your-model>.json
```

## A new product of a brand already here

Most of the time this is all there is. A manufacturer's protocol covers its whole range, so the
register map is written once and each product is a short file that points at it. Copy
`defs/powerguard/hp-10212.json` and change the model and its details (the values below are an example, not a real product):

```json
{
  "producer": "powerguard",
  "producerName": "PowerGuard",
  "model": "hp-20224",
  "modelName": "HP-20224",
  "kind": "inverter",
  "uses": "modbus-v1.1",
  "details": [
    "Rated power: 2 kW",
    "Battery: 24 V DC"
  ]
}
```

- **File name:** `model`, plus `.json`, in the brand's folder: `defs/powerguard/hp-20224.json`.
  Lower-case letters, digits and dashes.
- **`modelName`** is what the **Model** list shows.
- **`uses`** is the register map it reads with: the map's `model`, in the same brand folder. A
  product file holds no registers of its own, and one that does is refused.
- **`details`** are the lines the **?** beside Model shows in the form, up to 20 of up to 120
  characters each. Optional.

The map itself stays in the Model list as "Other model (Modbus protocol V1.1)", for a unit whose
product is not listed yet. Only write a new map, as below, for a product that does **not** speak
its brand's existing protocol.

## A new brand, or a new register map

1. **Copy the existing map.** Start from `internal/power/model/defs/powerguard/modbus-v1.1.json`. It
   is a complete, working example.
2. **Put the copy where it belongs.** The folder is the brand's id and the file name is the model's
   id. Use lower-case letters, digits and dashes, for example `defs/acme/ups-3k.json`.
3. **Fill in the four names at the top.** `producer` must equal the folder name and `model` must
   equal the file name without `.json`; the file is refused otherwise.

   | key | what it is | shown as |
   |---|---|---|
   | `producer` | the brand's id, the folder name | - |
   | `producerName` | the brand as people write it | the **Brand** list |
   | `model` | the model's id, the file name | - |
   | `modelName` | the model as people write it | the **Model** list |
   | `kind` | `"inverter"` or `"ups"` | - |
   | `serial` | the RS485 settings, e.g. `"9600 8N1"` | the unit's page |
   | `details` | optional technical details, one line each | the form's **?** |

   A new model of an existing brand goes in that brand's folder with the **same** `producer` and
   `producerName`, and it appears under that brand in the form.
4. **Describe the registers**, from the manufacturer's Modbus register table:
   - `reads` - the blocks of registers to ask for: `function` 3 (holding) or 4 (input), the first
     register `start`, and how many, `count` (at most 125). Only reads exist; nothing can write.
   - `fields` - which register holds which value. `div` divides the raw number: a register in
     "0.1 V" steps gets `"div": 10`. Add `"signed": true` for a value that can be negative.
     The names you may use are fixed, because the page, the alerts and the history read them:

     | key | meaning |
     |---|---|
     | `input_v`, `input_hz` | mains input voltage and frequency |
     | `output_v`, `output_hz`, `output_a` | output voltage, frequency and current |
     | `load_pct` | output load, % of rated |
     | `battery_v`, `battery_pct` | battery voltage and capacity |
     | `dc_bus_a` | DC bus current |
     | `temp_internal`, `temp_ambient` | temperatures, °C |

     A model that does not report a value simply leaves it out; the page shows a dash.
   - `flags` - the status register, and which bit means what: `mains_ok`, `charger_on`,
     `inverter_on`, `output_on` (bit 0 is the lowest).
   - `raw` - registers shown as plain numbers: `warning_bits`, `error_bits`.
   - `event` - the register holding the current event code, the text of every code (`codes`), and
     which codes are **not** a fault (`notFault`: "no event", notices such as ECO starting).
5. **Check it.** A wrong file is refused rather than half-read: an unknown key (a typo), a name not
   in the tables above, a register outside every read, or a repeated name each fail the check
   `TestEveryEmbeddedModelLoads`. It runs in the repository's **Test** workflow on GitHub (Actions
   must be enabled for the fork), or locally:

   ```bash
   docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go test ./internal/power/model/
   ```
6. **Release it.** The model files are built into the program, so a new model reaches a running
   MikroDash with the next image: build it (or publish a release) and redeploy with
   `docker compose up -d`.

## Things to know

- **Never rename or move a file once units use it.** A unit stores its model as
  `producer/model` (for example `powerguard/modbus-v1.1`); renaming the file leaves those units
  pointing at a model that no longer exists.
- **Test against a real unit first.** `cmd/powerprobe` reads one unit through the new file and
  prints every decoded value (`-raw` adds every register as read), without adding anything to
  MikroDash. Compare what it prints with the unit's own display:

  ```bash
  docker run --rm --network host -v "$PWD":/src -w /src golang:1.27-alpine \
    go run ./cmd/powerprobe -host 192.168.20.83 -slave 1 -model acme/ups-3k -raw
  ```
