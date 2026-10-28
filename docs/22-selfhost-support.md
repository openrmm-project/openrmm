# OpenRMM support for self-hosted consoles

OpenRMM is free software (AGPL-3.0) and stays free when you run it yourself.
An OpenRMM support plan, bought at [openrmm.io](https://openrmm.io), puts the
people who build it behind your installation:

- answers to questions and problems with OpenRMM, with an 8-hour response time,
  24 hours a day;
- help installing and upgrading;
- security advisories by email before they are published;
- help migrating to OpenRMM Cloud, if you decide to;
- priority for the bugs you report and the improvements you suggest.

The plan does not cover your servers, operating system, network, Docker or
backups, or custom development.

## Linking the console to the plan

The plan covers a number of devices, and the console reports how many it
manages. To link it:

1. In the portal, on your support plan, generate a **support key**. It starts
   with `ors_` and is shown once.
2. In the console, as an administrator, open **Settings ▸ Support**, paste the
   key and save. The console checks it with the portal before keeping it, and
   stores it encrypted (`RMM_SETTINGS_ENCRYPTION_KEY`).

Generating a new key in the portal replaces the old one: paste the new key in
the console. **Remove key** stops the reports at once.

One plan can cover several consoles (two sites, for example): paste the same key
in each, and their devices are added together. Up to 20 consoles can report with
one key.

## What the console sends

Only while a key is configured, every 6 hours, to
`https://openrmm.io/api/support/usage`:

| field | value |
|---|---|
| `devices_used` | how many devices the console manages |
| `version` | the console's version |
| `instance_id` | a random id made by the console, so the portal can tell one console from two sharing a key |

Nothing else: no device names, users, addresses or data. **A console without a
key never contacts the portal**; every self-hosted console starts that way.

`RMM_SUPPORT_ENDPOINT` points the reports at another portal (staging). It must
be an `https://` URL. Requests never follow a redirect, because they carry the
key.

## When the fleet outgrows the plan

The plan never limits the console: devices keep enrolling and working. When
the reported count stays above the plan for three days, the portal emails the
plan's owner, and the console shows administrators a banner with the date by
which the plan should be adjusted:

- **monthly plans** are not renewed if the plan is not adjusted;
- **annual plans** 20% or more above the plan end their support, without
  refund, if the plan is not adjusted (paying the difference) within 14 days.

The terms of service at openrmm.io/terms are the reference.

## OpenRMM Cloud consoles

A Cloud console has support in its plan. **Settings ▸ Support** says so and
offers no key field; tickets are opened from the portal.
