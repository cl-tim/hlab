FROM ubuntu:24.04
RUN apt-get update && apt-get install -y openssh-server sudo iproute2 \
    && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /run/sshd \
    && useradd -m -s /bin/bash labadmin \
    && echo 'labadmin:testpass123' | chpasswd \
    && usermod -aG sudo labadmin
# Password auth on, so the enrolment path under test is exercised.
RUN sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config \
    && sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
