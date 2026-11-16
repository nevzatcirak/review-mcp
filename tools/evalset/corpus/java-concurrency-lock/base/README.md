# wallet

Store credit of the example shop's customers. A wallet is used from the
checkout threads and from background jobs at the same time; all of its
state is guarded by the wallet's lock.

    mvn test
