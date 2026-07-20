---
type: Table
title: dim_channel
description: Sales channel dimension.
resource: table://ANALYTICS/MAIN/DIM_CHANNEL
tags:
  - table
---

# dim_channel

Sales channel dimension.

## Primary key

- `channel_id`

## Dimensions

- `channel_id` (number): Channel surrogate key.
- `channel_name` (varchar): Channel display name.

## Joins

- [fct_orders](/tables/fct_orders.md): `fct_orders.channel_id` = `dim_channel.channel_id`
