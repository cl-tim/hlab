# Plus4 Box heater refresh review

Reviewed 2026-11-23 against stock Plus4 2.1.1 and
[Valve-Oil c89dddc](https://github.com/thelegendtubaguy/Valve-Oil/blob/c89dddc475b73e060027ff5a2489239df1b14406/installer/klipper/extras/box_heater_refresh.py).
This review adds no heater patch, PWM setting, firmware image or active heat test.

## Timing

Valve-Oil describes an unchanged-output refresh of 3.75 seconds against a
five-second output timeout. The reviewed Plus4 host instead sets
`MAX_HEAT_TIME = 10.0`, passes it to `setup_max_duration`, and advances
`next_pwm_time` by `pwm_delay + 0.75 * MAX_HEAT_TIME`.
[Plus4 heater source](https://github.com/QIDITECH/klipper/blob/4bb7c6337936ef273e621d1f55bc0ef92114785d/klippy/extras/heaters.py#L14-L79).

The MCU pin builder converts that duration to MCU clock ticks and includes it
in the hardware-PWM or software-PWM configuration command.
[MCU pin source](https://github.com/QIDITECH/klipper/blob/4bb7c6337936ef273e621d1f55bc0ef92114785d/klippy/mcu.py).
This establishes the host-requested timeout, not every independent watchdog
inside a proprietary Box firmware image.

A synthetic harness exercised the stock `Heater.set_pwm` with a recording PWM
object, a fixed positive target and output, and a 0.1-second PWM delay. At
0.1-second sensor intervals, successive output commands were 7.6 seconds apart;
at 0.7-second intervals they were 7.7 seconds apart. An isolated
`MCU_pwm._build_config` with a recording MCU confirmed a ten-second configured
duration. No real printer object, IO pin or sensor was used in these simulations.

Valve-Oil's one-second refresh shortens the suppression deadline after a PWM
command was actually sent. It preserves output values and the safety timeout.
It could increase tolerance of host stalls, but it cannot guarantee progress
during a blocked sensor callback or reactor. The saved-variable improvement
also removes avoidable synchronous IO from this loop.

## Sensor shutdown errors

Valve-Oil separately guards the Box sensor timer: normal-operation exceptions
still propagate; an exception after shutdown stops polling with `reactor.NEVER`.
An isolated instance of the stock compiled AHT20 class, with its measurement
method replaced by a synthetic failure, propagated the error both before and
after a simulated shutdown. No sensor constructor or I2C command ran. This
supports compatibility investigation of the guard, but does not demonstrate
that it has masked a real shutdown on a particular printer.

## Decision

Keep the current heater implementation for now. A bounded review of recent
log tails did not establish a refresh-related fault; older history and actual
heated operation were not exhaustively tested. If adopted later, split out a
source-checked, opt-in host extension through the existing recovery transaction:

- Shorten only Box heater refresh suppression, preserving duty values,
  `MAX_HEAT_TIME`, thermal verification and all other heaters.
- Keep the shutdown-only sensor guard separate from normal error handling.
- Test both paths with synthetic timers and commands before any supervised
  physical validation.
- Leave PWM cycle-time tuning and firmware flashing outside this change.

Do not describe this Plus4 as having Valve-Oil's five-second timeout without
separate evidence for the installed Box firmware.
