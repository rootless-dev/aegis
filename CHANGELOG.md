# Changelog

## [0.0.5](https://github.com/rootless-dev/aegis/compare/v0.0.4...v0.0.5) (2026-08-26)


### Features

* add the realm aggregate ([19847dd](https://github.com/rootless-dev/aegis/commit/19847ddfd6ada642f15b5c123071a1d2c8db02eb))
* add the realm use cases and the ports they consume ([125eab1](https://github.com/rootless-dev/aegis/commit/125eab1fd5fbfcbc62f1cacda7e64472326e9959))
* configure how the schema reaches the version the binary carries ([9e96e9d](https://github.com/rootless-dev/aegis/commit/9e96e9d38dc75cf011db49fa9bbefd3bf830ab2a))
* give aegisd a migrate subcommand ([157edca](https://github.com/rootless-dev/aegis/commit/157edca429144f9ed813a64082be9c6fe964e1f2))
* map realms to the four dialects behind a transactional store ([bcda071](https://github.com/rootless-dev/aegis/commit/bcda071645e04b0eacc9f5bec371ad97de331d46))
* migrate, verify and seed the master realm during boot ([ecc6507](https://github.com/rootless-dev/aegis/commit/ecc6507e5ef19ddca6b0ef0d493d8c8778aa100e))
* migrations on boot, the realms table, and the master realm ([0d14fd5](https://github.com/rootless-dev/aegis/commit/0d14fd59d672fe9846c59ea859bf06d9bb366ed5))
* own the schema SQL and verify the version on every boot ([b733c6c](https://github.com/rootless-dev/aegis/commit/b733c6c961b5752b02af4d28a65c640ec1bfa0cb))


### Refactoring

* clear the smells the quality gate flagged ([6ecee02](https://github.com/rootless-dev/aegis/commit/6ecee02acb8b5e3930731213bc30486767f131a2))
* say realm where the product means a realm ([acf5d04](https://github.com/rootless-dev/aegis/commit/acf5d0494f3fbd50b6d05db3bcef7a0bd2f545ed))


### Documentation

* describe the migration subsystem and the domain layering ([3d7682f](https://github.com/rootless-dev/aegis/commit/3d7682f8868f91330815add6986c923ac002631b))
* record what phase 1 has landed in the roadmap ([0b2186c](https://github.com/rootless-dev/aegis/commit/0b2186ca3d312309362d2b21a32ca94716ba1516))

## [0.0.4](https://github.com/rootless-dev/aegis/compare/v0.0.3...v0.0.4) (2026-08-25)


### Features

* default the development profile to plain HTTP ([f9d8cb1](https://github.com/rootless-dev/aegis/commit/f9d8cb1d1769475f2c1b02fb1b0db007ba52a19b))
* render pages from templates embedded in the binary ([2ffc25b](https://github.com/rootless-dev/aegis/commit/2ffc25b70e488cc51755add8178ccddc810bc0c7))
* serve the HTML surface from the binary ([f8af6b7](https://github.com/rootless-dev/aegis/commit/f8af6b770002ce777e916527201ba8609e0aecf3))
* serve the page surface beside the JSON one ([0e6a774](https://github.com/rootless-dev/aegis/commit/0e6a774d8e8778357894290105d78e30e446a487))


### Documentation

* describe the page surface, the asset pipeline and the plain HTTP default ([d0ccb0e](https://github.com/rootless-dev/aegis/commit/d0ccb0ed9594a036d14e7548127e60f5553580c8))

## [0.0.3](https://github.com/rootless-dev/aegis/compare/v0.0.2...v0.0.3) (2026-08-23)


### Features

* **app:** open the database at boot and report it in readiness ([31669e2](https://github.com/rootless-dev/aegis/commit/31669e2dcbb6bc31c7efdc299a1f6e608117af60))
* **app:** wire TLS, certificate rotation and proxy trust ([28cd4fa](https://github.com/rootless-dev/aegis/commit/28cd4fa4b4d5feff7ab894180d4f624515b6a9f6))
* **certs:** serve TLS certificates from memory or rotating files ([6f3e1ad](https://github.com/rootless-dev/aegis/commit/6f3e1add20414897553726f162c8a090915f8b8a))
* **config:** add execution profiles and declare the TLS topology ([0746ddb](https://github.com/rootless-dev/aegis/commit/0746ddb3272a0d81cdb4cf853f60ec1111ce3d6f))
* **config:** add the database section ([00bc70d](https://github.com/rootless-dev/aegis/commit/00bc70d2a77552d4d5ff07a3f8da7f0fe64e96a2))
* connect aegis to the customer's database ([c00a70d](https://github.com/rootless-dev/aegis/commit/c00a70d03849ef2b96df50331268f47466921e08))
* **database:** add the connection factory for the four engines ([bb47d32](https://github.com/rootless-dev/aegis/commit/bb47d32cd07435f53e9449f5468d423440c1b412))
* **dev:** run the orchestrated loops against postgres ([6b5e634](https://github.com/rootless-dev/aegis/commit/6b5e634842a6ef2b44126766da88b5e89cd056a8))
* **health:** describe each readiness check and log what fails ([a17bfc8](https://github.com/rootless-dev/aegis/commit/a17bfc81533e2d0afd7c8f0f2abbf523282f44b9))
* **http:** resolve the client scheme and address behind a gateway ([2248154](https://github.com/rootless-dev/aegis/commit/2248154c4dc9a1e22b08837acbb902a108d30681))
* **http:** serve TLS from the server ([b1bf1db](https://github.com/rootless-dev/aegis/commit/b1bf1db61bd024fd5b880b71814cd6443733280a))
* TLS termination, execution profiles and proxy awareness ([63c18bd](https://github.com/rootless-dev/aegis/commit/63c18bdc98d00a1dcaac18e002317ffaceb1aa7d))


### Documentation

* document profiles, TLS termination and proxy trust ([c7dfd2b](https://github.com/rootless-dev/aegis/commit/c7dfd2b3fe5ac7bc245d8810eeb8185a794d9ca9))
* document the database section and the database in the loop ([6cd539c](https://github.com/rootless-dev/aegis/commit/6cd539c195de9af473467d5357a99c20214bb151))
* **plan:** keep the design and the plan behind the database layer ([5fe5ec2](https://github.com/rootless-dev/aegis/commit/5fe5ec27bd50d5cd488489fccfb7a644ca2a35c2))

## [0.0.2](https://github.com/rootless-dev/aegis/compare/v0.0.1...v0.0.2) (2026-08-19)


### Features

* **app:** assemble and run the application ([f703fad](https://github.com/rootless-dev/aegis/commit/f703fad05d0a94e2c97a3c9a6087a8c314185316))
* **banner:** print a startup banner ([19b2a64](https://github.com/rootless-dev/aegis/commit/19b2a646ca5eeb1de778ced08b514dd7e661f185))
* **buildinfo:** report the identity of the running binary ([0178843](https://github.com/rootless-dev/aegis/commit/0178843bac0a48c128d10ad2350efaba2786c04d))
* **config:** add layered configuration ([da7494e](https://github.com/rootless-dev/aegis/commit/da7494e19d3ff15c0c5609a074b4c1be04cffe23))
* **graceful:** add ordered shutdown ([be0ea50](https://github.com/rootless-dev/aegis/commit/be0ea504d8242d688fc1791ac479a43f44677114))
* **health:** add liveness and readiness probes ([49578e2](https://github.com/rootless-dev/aegis/commit/49578e2b792022d78f6906a4f555f8f0916b9a8e))
* **http:** add server, middleware chain and response format ([7f478b4](https://github.com/rootless-dev/aegis/commit/7f478b43618c015d5d6f1f40f3d48fec80693c9f))
* **logging:** add structured logging ([c970dde](https://github.com/rootless-dev/aegis/commit/c970dde311d0f674a5c4dfcdc89b390cfb61fcb2))


### Bug Fixes

* **k8s:** pin the image tag in the base manifests ([c30939c](https://github.com/rootless-dev/aegis/commit/c30939c6d7e3952e108e1a9bcf849b46f64fabf2))


### Documentation

* add readme and documentation ([38d31c4](https://github.com/rootless-dev/aegis/commit/38d31c469c826c4cc2886a00ddd83d679e514e85))

## 0.0.1 (2026-08-19)


### Features

* **app:** assemble and run the application ([f703fad](https://github.com/rootless-dev/aegis/commit/f703fad05d0a94e2c97a3c9a6087a8c314185316))
* **banner:** print a startup banner ([19b2a64](https://github.com/rootless-dev/aegis/commit/19b2a646ca5eeb1de778ced08b514dd7e661f185))
* **buildinfo:** report the identity of the running binary ([0178843](https://github.com/rootless-dev/aegis/commit/0178843bac0a48c128d10ad2350efaba2786c04d))
* **config:** add layered configuration ([da7494e](https://github.com/rootless-dev/aegis/commit/da7494e19d3ff15c0c5609a074b4c1be04cffe23))
* **graceful:** add ordered shutdown ([be0ea50](https://github.com/rootless-dev/aegis/commit/be0ea504d8242d688fc1791ac479a43f44677114))
* **health:** add liveness and readiness probes ([49578e2](https://github.com/rootless-dev/aegis/commit/49578e2b792022d78f6906a4f555f8f0916b9a8e))
* **http:** add server, middleware chain and response format ([7f478b4](https://github.com/rootless-dev/aegis/commit/7f478b43618c015d5d6f1f40f3d48fec80693c9f))
* **logging:** add structured logging ([c970dde](https://github.com/rootless-dev/aegis/commit/c970dde311d0f674a5c4dfcdc89b390cfb61fcb2))


### Bug Fixes

* **k8s:** pin the image tag in the base manifests ([c30939c](https://github.com/rootless-dev/aegis/commit/c30939c6d7e3952e108e1a9bcf849b46f64fabf2))


### Documentation

* add readme and documentation ([38d31c4](https://github.com/rootless-dev/aegis/commit/38d31c469c826c4cc2886a00ddd83d679e514e85))
