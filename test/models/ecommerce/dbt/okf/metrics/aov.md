---
type: Metric
title: Average order value
description: Average order value (net revenue / orders).
tags:
  - metric
---

# Average order value

Average order value (net revenue / orders).

## Definition

```sql
sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)
```

Defined on [fct_orders](/tables/fct_orders.md).
