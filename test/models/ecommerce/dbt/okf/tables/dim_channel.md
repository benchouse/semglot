---
type: Table
resource: table://ANALYTICS/MAIN/DIM_CHANNEL
title: dim_channel
description: Sales channel dimension.
tags:
  - table
timestamp: "2026-07-20T00:00:00+00:00"
---

# Overview

Sales channel dimension.

# Primary key

- `channel_id`

# Dimensions

- `channel_id` (number): Channel surrogate key.
- `channel_name` (varchar): Channel display name.

# Joins

- [fct_orders](fct_orders.md): `fct_orders.channel_id` = `dim_channel.channel_id`
